// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// ============================================================================
// Native Zero-CGO Multi-Format Document Stream Reader (`docreader.go`)
//
// Supported File Families (100% Pure Go Standard Library, Zero External Deps):
//   1. Word (`.docx`)            : `archive/zip` + `encoding/xml` (`word/document.xml` <w:p>, <w:tbl>)
//   2. Excel (`.xlsx`)           : `archive/zip` + `encoding/xml` (`xl/sharedStrings.xml` + `xl/worksheets/sheet*.xml`)
//   3. PowerPoint (`.pptx`)      : `archive/zip` + `encoding/xml` (`ppt/slides/slide*.xml` <a:t>)
//   4. PDF (`.pdf`)              : Pure Go PDF stream parser (`/FlateDecode` zlib + `BT..ET` `Tj`/`TJ` operators)
//   5. Structured Data           : `.json`, `.jsonl`, `.csv`, `.tsv`, `.yaml`, `.yml`, `.toml`
//   6. Markdown / Plaintext / Code: `.md`, `.txt`, `.rst`, `.go`, `.py`, `.ts`, `.js`, `.vue`, `.java`, `.rs`, `.sql`, `.sh`, `.c`, `.cpp`, `.h`
//
// Strict Secrecy Invariant:
//   Imports ZERO `"math"` and ZERO proprietary algorithm packages (`TestZeroAlgorithmLeakInThinClientSource`).
// ============================================================================

const (
	MaxTextFileSizeBytes   int64 = 4 * 1024 * 1024  // 4 MB cap for plain text / source code files
	MaxOfficeFileSizeBytes int64 = 32 * 1024 * 1024 // 32 MB cap for .docx / .xlsx / .pptx / .pdf files

	// TargetChunkTokens bounds one chunk at 1024 tokens counted by the embedded `tokenz-r`
	// tokenizer (`internal/tokenzr`), the same unit every other local token budget in the product
	// uses. Counting tokens rather than characters keeps one chunk at a comparable content mass
	// across scripts (a CJK character costs more tokens than a Latin letter).
	TargetChunkTokens int = 1024

	// maxOfficeEntryUncompressedBytes bounds one decompressed office XML entry: a
	// small archive can inflate far beyond its on-disk size, and the streaming
	// parsers would otherwise accumulate unbounded text from a decompression bomb.
	maxOfficeEntryUncompressedBytes int64 = 256 * 1024 * 1024
)

// FileSizeLimitError reports that a file exceeds the byte cap of its file family. Directory
// walks treat it as a per-file result — the file is excluded from the mount index for this
// pass — instead of aborting the whole directory sync, so one oversized artifact can never
// wedge a mount.
type FileSizeLimitError struct {
	AbsPath string
	Family  string // "office/pdf" or "text"
	Cap     int64
}

func (e *FileSizeLimitError) Error() string {
	return fmt.Sprintf("thinclient: %s file exceeds %d bytes cap: %s", e.Family, e.Cap, e.AbsPath)
}

// DocumentChunk represents a single logical segment extracted on-demand from a local file.
type DocumentChunk struct {
	ChunkIndex int      `json:"chunk_index"`
	Anchor     string   `json:"anchor"` // e.g., "p1", "Sheet1:r1-15", "page2", "L1-60"
	Text       string   `json:"text"`
	Sentences  []string `json:"sentences"`
	LineStart  int      `json:"line_start,omitempty"`
	LineEnd    int      `json:"line_end,omitempty"`
	FileURL    string   `json:"file_url,omitempty"`
	FileParse  string   `json:"file_parse,omitempty"`
}

// IsSupportedBackgroundFile reports whether `filePath` has a supported document or code extension
// and is not a temporary/hidden lock file (such as `~$foo.docx` or `.DS_Store`).
func IsSupportedBackgroundFile(filePath string) bool {
	base := filepath.Base(filePath)
	if base == "" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "~$") {
		return false
	}
	switch strings.ToLower(base) {
	case "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "composer.lock", "cargo.lock", "poetry.lock":
		return false
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".docx", ".xlsx", ".pptx", ".pdf",
		".txt", ".md", ".markdown", ".rst", ".org",
		".json", ".jsonl", ".csv", ".tsv", ".yaml", ".yml", ".toml", ".ini", ".xml", ".html", ".htm",
		".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte",
		".java", ".kt", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".swift", ".rb", ".php", ".lua",
		".sql", ".sh", ".bash", ".zsh", ".dockerfile", ".makefile", ".proto", ".graphql", ".css", ".scss":
		return true
	default:
		return false
	}
}

// ShouldSkipBackgroundDir reports whether a directory name should be ignored during recursive directory scanning.
func ShouldSkipBackgroundDir(dirName string) bool {
	if dirName == "" {
		return true
	}
	if strings.HasPrefix(dirName, ".") && dirName != "." {
		return true
	}
	switch strings.ToLower(dirName) {
	case "node_modules", "vendor", "dist", "build", "target", "out", "bin", "obj",
		"__pycache__", ".venv", "venv", ".git", ".svn", ".hg", ".idea", ".vscode", ".zcode",
		"coverage", "tmp", "temp", "logs":
		return true
	default:
		return false
	}
}

// ReadAndChunkLocalFile reads a local file on demand according to its extension and splits it into
// deterministic `DocumentChunk`s. Each chunk opens with the reader coordinate
// `[rel_path#anchor] (abs_path)`: the relative path plus section/page/row/line anchor names the
// reading position, and the absolute path lets any consumer (console preview, recall delivery,
// harness agent) open the exact source file without another lookup. Line-structured bodies stay
// 1:1 with the source's own line numbering, so `L<start>-<end>` anchors map to file:line directly.
func ReadAndChunkLocalFile(absPath string, relDisplayPath string) ([]DocumentChunk, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("thinclient: %s is a directory", absPath)
	}
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".docx", ".xlsx", ".pptx", ".pdf":
		if info.Size() > MaxOfficeFileSizeBytes {
			return nil, &FileSizeLimitError{AbsPath: absPath, Family: "office/pdf", Cap: MaxOfficeFileSizeBytes}
		}
	default:
		if info.Size() > MaxTextFileSizeBytes {
			return nil, &FileSizeLimitError{AbsPath: absPath, Family: "text", Cap: MaxTextFileSizeBytes}
		}
	}

	// Chunk sizing needs the tokenz-r counter. `tokenzr.Count` returns 0 when the embedded
	// vocabulary cannot be loaded, which would silently collapse every file into one unbounded
	// chunk, so the reader fails the file instead of indexing a mis-sized body.
	if _, err := tokenzr.Default(); err != nil {
		return nil, fmt.Errorf("thinclient: tokenz-r tokenizer unavailable: %w", err)
	}

	if relDisplayPath == "" {
		relDisplayPath = filepath.Base(absPath)
	}
	relDisplayPath = filepath.ToSlash(relDisplayPath)

	switch ext {
	case ".docx":
		return readDocxChunks(absPath, relDisplayPath)
	case ".xlsx":
		return readXlsxChunks(absPath, relDisplayPath)
	case ".pptx":
		return readPptxChunks(absPath, relDisplayPath)
	case ".pdf":
		return readPDFChunks(absPath, relDisplayPath)
	case ".csv", ".tsv":
		return readCSVChunks(absPath, relDisplayPath, ext == ".tsv")
	case ".json", ".jsonl":
		return readJSONChunks(absPath, relDisplayPath, ext == ".jsonl")
	default:
		return readTextOrCodeChunks(absPath, relDisplayPath)
	}
}

type rawChunkBlock struct {
	anchor    string
	lines     []string
	lineStart int
	lineEnd   int
}

func finalizeDocumentChunks(absPath string, relPath string, rawBlocks []rawChunkBlock) []DocumentChunk {
	var out []DocumentChunk
	curLine := 1
	for _, blk := range rawBlocks {
		// Keep one rendered line per source line (blank lines included) so the
		// chunk body stays 1:1 with the file's own line numbering.
		hasContent := false
		cleaned := make([]string, 0, len(blk.lines))
		for _, l := range blk.lines {
			t := strings.TrimSpace(l)
			cleaned = append(cleaned, t)
			if t != "" {
				hasContent = true
			}
		}
		if !hasContent {
			continue
		}
		lStart := blk.lineStart
		lEnd := blk.lineEnd
		if lStart <= 0 {
			lStart = curLine
		}
		if lEnd < lStart {
			lEnd = lStart + len(cleaned) - 1
		}
		curLine = lEnd + 1
		parsedBody := strings.Join(cleaned, "\n")
		header := fmt.Sprintf("[%s#%s]", relPath, blk.anchor)
		if trimmedAbs := strings.TrimSpace(absPath); trimmedAbs != "" {
			header = fmt.Sprintf("[%s#%s] (%s)", relPath, blk.anchor, trimmedAbs)
		}
		body := header + "\n" + parsedBody
		sents := SplitSentences(body)
		if len(sents) == 0 {
			sents = []string{body}
		}
		out = append(out, DocumentChunk{
			ChunkIndex: len(out),
			Anchor:     blk.anchor,
			Text:       body,
			Sentences:  sents,
			LineStart:  lStart,
			LineEnd:    lEnd,
			FileURL:    relPath,
			FileParse:  parsedBody,
		})
	}
	return out
}

// measureLineTokens returns the tokenz-r token count of one rendered line; the chunk budget is
// expressed in these tokens so every script shares one sizing unit.
func measureLineTokens(s string) int {
	return tokenzr.Count(s)
}

// ============================================================================
// 1. Word (`.docx`) Reader — Pure Go `archive/zip` + `encoding/xml`
// ============================================================================

func readDocxChunks(absPath string, relPath string) ([]DocumentChunk, error) {
	zr, err := zip.OpenReader(absPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var paragraphs []string
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if name == "word/document.xml" || strings.HasPrefix(name, "word/header") || strings.HasPrefix(name, "word/footer") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			pars := parseWordXMLParagraphs(io.LimitReader(rc, maxOfficeEntryUncompressedBytes))
			rc.Close()
			paragraphs = append(paragraphs, pars...)
		}
	}
	if len(paragraphs) == 0 {
		return nil, nil
	}
	return groupLinesIntoChunks(absPath, relPath, "p", paragraphs), nil
}

func parseWordXMLParagraphs(r io.Reader) []string {
	dec := xml.NewDecoder(r)
	var paragraphs []string
	var curPara strings.Builder
	inPara := false
	inText := false

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "p":
				inPara = true
				curPara.Reset()
			case "t":
				if inPara {
					inText = true
				}
			case "tab":
				if inPara {
					curPara.WriteString("\t")
				}
			case "br", "cr":
				if inPara {
					curPara.WriteString("\n")
				}
			case "tc":
				if inPara && curPara.Len() > 0 {
					curPara.WriteString(" | ")
				}
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "t":
				inText = false
			case "p":
				if inPara {
					txt := strings.TrimSpace(curPara.String())
					if txt != "" {
						paragraphs = append(paragraphs, txt)
					}
					inPara = false
				}
			}
		case xml.CharData:
			if inPara && inText {
				curPara.Write(se)
			}
		}
	}
	return paragraphs
}

// ============================================================================
// 2. Excel (`.xlsx`) Reader — Pure Go `archive/zip` + `encoding/xml`
// ============================================================================

func readXlsxChunks(absPath string, relPath string) ([]DocumentChunk, error) {
	zr, err := zip.OpenReader(absPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var sharedStrings []string
	sheetNames := map[string]string{}
	var sheetFiles []*zip.File

	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		switch {
		case name == "xl/sharedStrings.xml":
			if rc, err := f.Open(); err == nil {
				sharedStrings = parseXlsxSharedStrings(io.LimitReader(rc, maxOfficeEntryUncompressedBytes))
				rc.Close()
			}
		case name == "xl/workbook.xml":
			if rc, err := f.Open(); err == nil {
				sheetNames = parseXlsxWorkbookSheetNames(io.LimitReader(rc, maxOfficeEntryUncompressedBytes))
				rc.Close()
			}
		case strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml"):
			sheetFiles = append(sheetFiles, f)
		}
	}

	sort.Slice(sheetFiles, func(i, j int) bool {
		return sheetFiles[i].Name < sheetFiles[j].Name
	})

	var blocks []rawChunkBlock
	for idx, sf := range sheetFiles {
		rc, err := sf.Open()
		if err != nil {
			continue
		}
		rows := parseXlsxWorksheetRows(io.LimitReader(rc, maxOfficeEntryUncompressedBytes), sharedStrings)
		rc.Close()
		if len(rows) == 0 {
			continue
		}
		baseName := strings.TrimSuffix(filepath.Base(sf.Name), ".xml")
		sheetTitle := sheetNames[baseName]
		if sheetTitle == "" {
			sheetTitle = fmt.Sprintf("Sheet%d", idx+1)
		}

		headerCols := rows[0]
		var curLines []string
		curTokens := 0
		startRow := 1
		for rIdx, row := range rows {
			var lineParts []string
			for cIdx, val := range row {
				val = strings.TrimSpace(val)
				if val == "" {
					continue
				}
				if rIdx > 0 && cIdx < len(headerCols) && strings.TrimSpace(headerCols[cIdx]) != "" {
					lineParts = append(lineParts, strings.TrimSpace(headerCols[cIdx])+": "+val)
				} else {
					lineParts = append(lineParts, val)
				}
			}
			if len(lineParts) == 0 {
				continue
			}
			lineStr := fmt.Sprintf("[%s Row %d] %s", sheetTitle, rIdx+1, strings.Join(lineParts, " | "))
			w := measureLineTokens(lineStr)
			if curTokens+w > TargetChunkTokens && len(curLines) > 0 {
				endRow := rIdx
				if endRow < startRow {
					endRow = startRow
				}
				blocks = append(blocks, rawChunkBlock{
					anchor:    fmt.Sprintf("%s:r%d-%d", sheetTitle, startRow, rIdx),
					lines:     curLines,
					lineStart: startRow,
					lineEnd:   endRow,
				})
				curLines = nil
				curTokens = 0
				startRow = rIdx + 1
			}
			curLines = append(curLines, lineStr)
			curTokens += w
		}
		if len(curLines) > 0 {
			endRow := len(rows)
			if endRow < startRow {
				endRow = startRow
			}
			blocks = append(blocks, rawChunkBlock{
				anchor:    fmt.Sprintf("%s:r%d-%d", sheetTitle, startRow, len(rows)),
				lines:     curLines,
				lineStart: startRow,
				lineEnd:   endRow,
			})
		}
	}
	return finalizeDocumentChunks(absPath, relPath, blocks), nil
}

func parseXlsxSharedStrings(r io.Reader) []string {
	dec := xml.NewDecoder(r)
	var out []string
	var cur strings.Builder
	inSI := false
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if se.Name.Local == "si" {
				inSI = true
				cur.Reset()
			} else if inSI && se.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			if se.Name.Local == "t" {
				inT = false
			} else if se.Name.Local == "si" {
				out = append(out, cur.String())
				inSI = false
			}
		case xml.CharData:
			if inSI && inT {
				cur.Write(se)
			}
		}
	}
	return out
}

func parseXlsxWorkbookSheetNames(r io.Reader) map[string]string {
	dec := xml.NewDecoder(r)
	out := map[string]string{}
	idx := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			name := ""
			for _, attr := range se.Attr {
				if attr.Name.Local == "name" {
					name = strings.TrimSpace(attr.Value)
				}
			}
			if name != "" {
				out[fmt.Sprintf("sheet%d", idx)] = name
				idx++
			}
		}
	}
	return out
}

func parseXlsxWorksheetRows(r io.Reader, shared []string) [][]string {
	dec := xml.NewDecoder(r)
	var rows [][]string
	var curRow []string
	var cellType string
	var cellVal strings.Builder
	inCell := false
	inVal := false

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "row":
				curRow = nil
			case "c":
				inCell = true
				cellType = ""
				cellVal.Reset()
				for _, attr := range se.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			case "v", "t":
				if inCell {
					inVal = true
				}
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "v", "t":
				inVal = false
			case "c":
				if inCell {
					raw := strings.TrimSpace(cellVal.String())
					val := raw
					if cellType == "s" {
						if sIdx, err := strconv.Atoi(raw); err == nil && sIdx >= 0 && sIdx < len(shared) {
							val = shared[sIdx]
						}
					}
					curRow = append(curRow, val)
					inCell = false
				}
			case "row":
				nonEmpty := false
				for _, c := range curRow {
					if strings.TrimSpace(c) != "" {
						nonEmpty = true
						break
					}
				}
				if nonEmpty {
					rows = append(rows, curRow)
				}
			}
		case xml.CharData:
			if inCell && inVal {
				cellVal.Write(se)
			}
		}
	}
	return rows
}

// ============================================================================
// 3. PowerPoint (`.pptx`) Reader — Pure Go `archive/zip` + `encoding/xml`
// ============================================================================

func readPptxChunks(absPath string, relPath string) ([]DocumentChunk, error) {
	zr, err := zip.OpenReader(absPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var slideFiles []*zip.File
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
			slideFiles = append(slideFiles, f)
		}
	}
	sort.Slice(slideFiles, func(i, j int) bool {
		return slideFiles[i].Name < slideFiles[j].Name
	})

	var blocks []rawChunkBlock
	curLine := 1
	for idx, sf := range slideFiles {
		rc, err := sf.Open()
		if err != nil {
			continue
		}
		lines := parsePptxSlideXML(io.LimitReader(rc, maxOfficeEntryUncompressedBytes))
		rc.Close()
		if len(lines) > 0 {
			endLine := curLine + len(lines) - 1
			blocks = append(blocks, rawChunkBlock{
				anchor:    fmt.Sprintf("slide%d", idx+1),
				lines:     lines,
				lineStart: curLine,
				lineEnd:   endLine,
			})
			curLine = endLine + 1
		}
	}
	return finalizeDocumentChunks(absPath, relPath, blocks), nil
}

func parsePptxSlideXML(r io.Reader) []string {
	dec := xml.NewDecoder(r)
	var lines []string
	var curPara strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if se.Name.Local == "p" {
				curPara.Reset()
			} else if se.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			if se.Name.Local == "t" {
				inT = false
			} else if se.Name.Local == "p" {
				txt := strings.TrimSpace(curPara.String())
				if txt != "" {
					lines = append(lines, txt)
				}
			}
		case xml.CharData:
			if inT {
				curPara.Write(se)
			}
		}
	}
	return lines
}

// ============================================================================
// 4. PDF (`.pdf`) Reader — Pure Go Stream & FlateDecode Text Operator Extractor
// ============================================================================

func readPDFChunks(absPath string, relPath string) ([]DocumentChunk, error) {
	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	streams := extractPDFContentStreams(rawBytes)
	var blocks []rawChunkBlock
	pageIdx := 1
	curLine := 1
	for _, st := range streams {
		lines := extractTextFromPDFContentStream(st)
		if len(lines) > 0 {
			endLine := curLine + len(lines) - 1
			blocks = append(blocks, rawChunkBlock{
				anchor:    fmt.Sprintf("page%d", pageIdx),
				lines:     lines,
				lineStart: curLine,
				lineEnd:   endLine,
			})
			pageIdx++
			curLine = endLine + 1
		}
	}
	return finalizeDocumentChunks(absPath, relPath, blocks), nil
}

func extractPDFContentStreams(pdfData []byte) [][]byte {
	var out [][]byte
	searchPos := 0
	for searchPos < len(pdfData) {
		sIdx := bytes.Index(pdfData[searchPos:], []byte("stream"))
		if sIdx < 0 {
			break
		}
		streamStart := searchPos + sIdx + 6
		// Ensure "stream" is not part of "endstream"
		if searchPos+sIdx >= 3 && bytes.Equal(pdfData[searchPos+sIdx-3:searchPos+sIdx], []byte("end")) {
			searchPos = streamStart
			continue
		}
		if streamStart < len(pdfData) && pdfData[streamStart] == '\r' {
			streamStart++
		}
		if streamStart < len(pdfData) && pdfData[streamStart] == '\n' {
			streamStart++
		} else {
			searchPos = streamStart
			continue
		}

		eIdx := bytes.Index(pdfData[streamStart:], []byte("endstream"))
		if eIdx < 0 {
			break
		}
		rawStream := bytes.TrimRight(pdfData[streamStart:streamStart+eIdx], "\r\n")

		// Check dictionary header before "stream" for /FlateDecode
		dictStart := searchPos + sIdx - 256
		if dictStart < 0 {
			dictStart = 0
		}
		dictHeader := pdfData[dictStart : searchPos+sIdx]
		if bytes.Contains(dictHeader, []byte("/FlateDecode")) {
			if zr, err := zlib.NewReader(bytes.NewReader(rawStream)); err == nil {
				decompressed, rErr := io.ReadAll(io.LimitReader(zr, MaxTextFileSizeBytes))
				zr.Close()
				if rErr == nil && len(decompressed) > 0 {
					out = append(out, decompressed)
				}
			}
		} else if bytes.Contains(rawStream, []byte("BT")) && bytes.Contains(rawStream, []byte("ET")) {
			out = append(out, rawStream)
		}
		searchPos = streamStart + eIdx + 9
	}
	return out
}

func extractTextFromPDFContentStream(stream []byte) []string {
	var lines []string
	pos := 0
	for pos < len(stream) {
		btIdx := bytes.Index(stream[pos:], []byte("BT"))
		if btIdx < 0 {
			break
		}
		blockStart := pos + btIdx + 2
		etIdx := bytes.Index(stream[blockStart:], []byte("ET"))
		if etIdx < 0 {
			break
		}
		block := stream[blockStart : blockStart+etIdx]
		pos = blockStart + etIdx + 2

		var curLine strings.Builder
		i := 0
		for i < len(block) {
			switch block[i] {
			case '(':
				str, nextI := parsePDFLiteralString(block, i)
				if str != "" {
					curLine.WriteString(str)
				}
				i = nextI
			case '<':
				if i+1 < len(block) && block[i+1] != '<' {
					str, nextI := parsePDFHexString(block, i)
					if str != "" {
						curLine.WriteString(str)
					}
					i = nextI
				} else {
					i += 2
				}
			case '\n', '\r':
				if curLine.Len() > 0 {
					curLine.WriteByte(' ')
				}
				i++
			default:
				// Check for line-break text positioning operators Td / TD / T*
				if block[i] == 'T' && i+1 < len(block) && (block[i+1] == 'd' || block[i+1] == 'D' || block[i+1] == '*') {
					txt := strings.TrimSpace(curLine.String())
					if txt != "" {
						lines = append(lines, txt)
						curLine.Reset()
					}
					i += 2
					continue
				}
				i++
			}
		}
		if txt := strings.TrimSpace(curLine.String()); txt != "" {
			lines = append(lines, txt)
		}
	}
	return lines
}

func parsePDFLiteralString(data []byte, start int) (string, int) {
	var buf bytes.Buffer
	depth := 0
	i := start
	for i < len(data) {
		b := data[i]
		if b == '\\' && i+1 < len(data) {
			next := data[i+1]
			switch next {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case '(', ')', '\\':
				buf.WriteByte(next)
			default:
				buf.WriteByte(next)
			}
			i += 2
			continue
		}
		if b == '(' {
			if depth > 0 {
				buf.WriteByte(b)
			}
			depth++
		} else if b == ')' {
			depth--
			if depth <= 0 {
				i++
				break
			}
			buf.WriteByte(b)
		} else if depth > 0 {
			buf.WriteByte(b)
		}
		i++
	}
	raw := buf.Bytes()
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return decodeUTF16BE(raw[2:]), i
	}
	return string(raw), i
}

func parsePDFHexString(data []byte, start int) (string, int) {
	i := start + 1
	var hexChars []byte
	for i < len(data) && data[i] != '>' {
		c := data[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			hexChars = append(hexChars, c)
		}
		i++
	}
	if i < len(data) && data[i] == '>' {
		i++
	}
	if len(hexChars)%2 == 1 {
		hexChars = append(hexChars, '0')
	}
	raw := make([]byte, len(hexChars)/2)
	for k := 0; k < len(raw); k++ {
		hi := fromHexChar(hexChars[2*k])
		lo := fromHexChar(hexChars[2*k+1])
		raw[k] = (hi << 4) | lo
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return decodeUTF16BE(raw[2:]), i
	}
	if utf8.Valid(raw) {
		return string(raw), i
	}
	return decodeUTF16BE(raw), i
}

func fromHexChar(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

func decodeUTF16BE(b []byte) string {
	u16 := make([]uint16, len(b)/2)
	for i := 0; i < len(u16); i++ {
		u16[i] = (uint16(b[2*i]) << 8) | uint16(b[2*i+1])
	}
	return string(utf16.Decode(u16))
}

// ============================================================================
// 5. CSV / TSV & JSON / JSONL Readers
// ============================================================================

func readCSVChunks(absPath string, relPath string, isTSV bool) ([]DocumentChunk, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	if isTSV {
		cr.Comma = '\t'
	}
	records, err := cr.ReadAll()
	if err != nil {
		// A partial parse must never be indexed as if it were the whole file;
		// plain line chunking keeps every row's text without pretending CSV structure.
		return readTextOrCodeChunks(absPath, relPath)
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	var lines []string
	for rIdx, row := range records {
		var parts []string
		for cIdx, val := range row {
			val = strings.TrimSpace(val)
			if val == "" {
				continue
			}
			if rIdx > 0 && cIdx < len(header) && strings.TrimSpace(header[cIdx]) != "" {
				parts = append(parts, strings.TrimSpace(header[cIdx])+": "+val)
			} else {
				parts = append(parts, val)
			}
		}
		if len(parts) > 0 {
			lines = append(lines, fmt.Sprintf("[Row %d] %s", rIdx+1, strings.Join(parts, " | ")))
		}
	}
	return groupLinesIntoChunks(absPath, relPath, "row", lines), nil
}

func readJSONChunks(absPath string, relPath string, isJSONL bool) ([]DocumentChunk, error) {
	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var lines []string
	if isJSONL {
		// JSONL lines often carry large tool outputs; the token cap matches the
		// file-family cap so no line can silently end the scan, and a scan failure
		// is surfaced instead of dropping everything after it.
		sc := bufio.NewScanner(bytes.NewReader(rawBytes))
		sc.Buffer(make([]byte, 64*1024), int(MaxTextFileSizeBytes))
		lineNo := 0
		for sc.Scan() {
			lineNo++
			txt := strings.TrimSpace(sc.Text())
			if txt == "" {
				continue
			}
			var val any
			if json.Unmarshal([]byte(txt), &val) == nil {
				flattenJSONValue(fmt.Sprintf("L%d", lineNo), val, &lines)
			} else {
				lines = append(lines, txt)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("thinclient: scan jsonl %s: %w", relPath, err)
		}
	} else {
		var val any
		if json.Unmarshal(rawBytes, &val) == nil {
			flattenJSONValue("root", val, &lines)
		} else {
			return readTextOrCodeChunks(absPath, relPath)
		}
	}
	return groupLinesIntoChunks(absPath, relPath, "json", lines), nil
}

func flattenJSONValue(prefix string, val any, out *[]string) {
	switch v := val.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			nextPrefix := k
			if prefix != "" && prefix != "root" {
				nextPrefix = prefix + "." + k
			}
			flattenJSONValue(nextPrefix, v[k], out)
		}
	case []any:
		for i, item := range v {
			flattenJSONValue(fmt.Sprintf("%s[%d]", prefix, i), item, out)
		}
	case string:
		if strings.TrimSpace(v) != "" {
			*out = append(*out, fmt.Sprintf("%s: %s", prefix, strings.TrimSpace(v)))
		}
	case float64, bool:
		*out = append(*out, fmt.Sprintf("%s: %v", prefix, v))
	}
}

// ============================================================================
// 6. Plaintext, Markdown & Source Code Reader
// ============================================================================

func readTextOrCodeChunks(absPath string, relPath string) ([]DocumentChunk, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	// The file-family cap already bounds the file; matching it here means a long
	// line can never silently truncate the scan, and a real scan failure is
	// surfaced instead of indexing an empty prefix.
	sc.Buffer(buf, int(MaxTextFileSizeBytes))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("thinclient: scan %s: %w", relPath, err)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return groupLinesIntoChunks(absPath, relPath, "L", lines), nil
}

func groupLinesIntoChunks(absPath string, relPath string, prefix string, lines []string) []DocumentChunk {
	var blocks []rawChunkBlock
	var curLines []string
	curTokens := 0
	startIdx := 1

	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		tokens := measureLineTokens(trimmed)
		isHeading := strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ")
		if (curTokens+tokens > TargetChunkTokens || (isHeading && curTokens >= TargetChunkTokens/3)) && len(curLines) > 0 {
			endIdx := idx
			if endIdx < startIdx {
				endIdx = startIdx
			}
			blocks = append(blocks, rawChunkBlock{
				anchor:    fmt.Sprintf("%s%d-%d", prefix, startIdx, idx),
				lines:     curLines,
				lineStart: startIdx,
				lineEnd:   endIdx,
			})
			curLines = nil
			curTokens = 0
			startIdx = idx + 1
		}
		// Blank lines carry zero tokens but stay in the block so the rendered chunk keeps the
		// source's line numbering intact from `lineStart` to `lineEnd`.
		curLines = append(curLines, trimmed)
		curTokens += tokens
	}
	if len(curLines) > 0 {
		endIdx := len(lines)
		if endIdx < startIdx {
			endIdx = startIdx
		}
		blocks = append(blocks, rawChunkBlock{
			anchor:    fmt.Sprintf("%s%d-%d", prefix, startIdx, len(lines)),
			lines:     curLines,
			lineStart: startIdx,
			lineEnd:   endIdx,
		})
	}
	return finalizeDocumentChunks(absPath, relPath, blocks)
}
