// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package rm 是新一代 Zhao 四步双簇（EntityCluster × SpacetimeCluster）检索内核的顶层统一门面（SSOT），
// 内部实现按正交领域子目录物理隔离维护：
//   - src/zhao/rm/closedset : 双层纯数据闭集世界知识同态表与零分配查找器
//   - src/zhao/rm/shaping   : 零先验纯字符几何规整与 O(K) 句内空格容错粘合原语
//   - src/zhao/rm/atmdata   : ATM-Bench 官方语料/题集加载器与 gold-set-mean Recall@k 算分器
//   - src/zhao/rm/codec     : 脉冲编码器、8-bit 结构标志位、词元装饰与词项抽取器
//   - src/zhao/rm/cluster   : 四步双簇检索内核、单文档五问关系自省、SQLite 存储与 .zcsr 磁盘 mmap 温层
//   - src/zhao/rm/eval      : 分词/双簇健康度体检仪、脉冲取数桥、5 域 20 项自维护对抗注册表及基准测试套件
//   - src/zhao/rm/cmd/zhaorm: CLI 命令行诊断与评测工具
package rm

import (
	"database/sql"

	"github.com/rocareer/rolling-memory/internal/zhao/rm/atmdata"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/closedset"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/cluster"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/codec"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/shaping"
)

type (
	CorpusDoc   = atmdata.CorpusDoc
	ATMItem     = atmdata.ATMItem
	ATMQuestion = atmdata.ATMQuestion
	TermRole    = cluster.TermRole
	Term        = cluster.Term

	TermPosition          = cluster.TermPosition
	DocMeta               = cluster.DocMeta
	Doc                   = cluster.Doc
	Posting               = cluster.Posting
	DocEntryView          = cluster.DocEntryView
	Index                 = cluster.Index
	EnergyMode            = cluster.EnergyMode
	Phase3Candidate       = cluster.Phase3Candidate
	Phase3Delta           = cluster.Phase3Delta
	Phase3Plugin          = cluster.Phase3Plugin
	QueryExtractor        = cluster.QueryExtractor
	Engine                = cluster.Engine
	Hit                   = cluster.Hit
	Store                 = cluster.Store
	StoreDialect          = cluster.StoreDialect
	StoreOptions          = cluster.StoreOptions
	DualClusterStore      = cluster.DualClusterStore
	UnitRelationPeer      = cluster.UnitRelationPeer
	UnitRelationProfile   = cluster.UnitRelationProfile
	HitClusterGroup       = cluster.HitClusterGroup
	OrdinalSpanAnnotation = codec.OrdinalSpanAnnotation
	UnitMeta              = codec.UnitMeta
	BlindTokenTerm        = codec.BlindTokenTerm
	BlindTokenAtom        = codec.BlindTokenAtom
	BlindPulsePacket      = codec.BlindPulsePacket

	HarmonicExpander         = codec.HarmonicExpander
	CanonicalHarmonicOptions = codec.CanonicalHarmonicOptions
	BlindEncoder             = codec.BlindEncoder
	SubUnit                  = codec.SubUnit
	TermSample               = codec.TermSample
	FoldedTerm               = codec.FoldedTerm
	PhraseDyad               = codec.PhraseDyad
	CrestPhrase              = codec.CrestPhrase
	DocumentFeatures         = codec.DocumentFeatures
	TermExtractor            = codec.TermExtractor

	SpaceElision     = codec.SpaceElision
	SpaceFaultFusion = codec.SpaceFaultFusion
	PulseSlot        = codec.PulseSlot
)

const (
	RolePrimary             = cluster.RolePrimary
	RoleMorphStem           = cluster.RoleMorphStem
	RoleSubwordPiece        = cluster.RoleSubwordPiece
	RoleClosedBeacon        = cluster.RoleClosedBeacon
	RoleBiwordHomomorph     = cluster.RoleBiwordHomomorph
	ExtFlagSeenPrimary      = cluster.ExtFlagSeenPrimary
	ExtFlagSeenMorphOrClos  = cluster.ExtFlagSeenMorphOrClos
	ExtFlagSeenSubword      = cluster.ExtFlagSeenSubword
	DefaultHop1Expansions   = cluster.DefaultHop1Expansions
	DefaultHop2Expansions   = cluster.DefaultHop2Expansions
	EnergyMutualInfo        = cluster.EnergyMutualInfo
	EnergyDensity           = cluster.EnergyDensity
	SQLiteSchema            = cluster.SQLiteSchema
	PostgresSchema          = cluster.PostgresSchema
	PostgresCipherSchema    = cluster.PostgresCipherSchema
	PostgresPlainSchema     = cluster.PostgresPlainSchema
	DialectSQLite           = cluster.DialectSQLite
	DialectPostgres         = cluster.DialectPostgres
	DialectPostgresCipher   = cluster.DialectPostgresCipher
	DialectPostgresPlain    = cluster.DialectPostgresPlain
	FlagHasUpper            = codec.FlagHasUpper
	FlagHasCamel            = codec.FlagHasCamel
	FlagHasDigit            = codec.FlagHasDigit
	FlagHasConn             = codec.FlagHasConn
	FlagIsHarmonicCompanion = codec.FlagIsHarmonicCompanion
	FlagIsFocalHeader       = codec.FlagIsFocalHeader
	FlagHasBoundaryQuote    = codec.FlagHasBoundaryQuote
	FlagIsQuantityOrUnit    = codec.FlagIsQuantityOrUnit
	FlagCompanionModifier   = codec.FlagCompanionModifier
	CompanionSlots          = codec.CompanionSlots
	CanonicalPhraseJoint    = shaping.CanonicalPhraseJoint
)

var (
	DemoSecret                     = codec.DemoSecret
	StandardClosedSetTable         = closedset.StandardClosedSetTable
	UniversalSchemaClosedSetTable  = closedset.UniversalSchemaClosedSetTable
	ClosedSetHarmonicEntries       = closedset.ClosedSetHarmonicEntries
	ErrInvalidZCSR                 = cluster.ErrInvalidZCSR
	ErrCorruptZCSR                 = cluster.ErrCorruptZCSR
	ErrCipherStoreRejectsPlaintext = cluster.ErrCipherStoreRejectsPlaintext
	ErrPlainStoreRequiresPlaintext = cluster.ErrPlainStoreRequiresPlaintext
	ErrDualStoreModeConflict       = cluster.ErrDualStoreModeConflict
)

func ATMFullText(item ATMItem) string { return atmdata.ATMFullText(item) }
func LoadATMCorpus(dataRoot string, limit int) ([]CorpusDoc, error) {
	return atmdata.LoadATMCorpus(dataRoot, limit)
}
func LoadATMQuestions(dataRoot, tier string, limit int) ([]ATMQuestion, error) {
	return atmdata.LoadATMQuestions(dataRoot, tier, limit)
}
func RecallAt(evidenceIDs, topIDs []string, k int) float64 {
	return atmdata.RecallAt(evidenceIDs, topIDs, k)
}
func AnyHitAt(evidenceIDs, topIDs []string, k int) bool {
	return atmdata.AnyHitAt(evidenceIDs, topIDs, k)
}
func AllHitAt(evidenceIDs, topIDs []string, k int) bool {
	return atmdata.AllHitAt(evidenceIDs, topIDs, k)
}
func HighDFThresholdForN(n int) int         { return cluster.HighDFThresholdForN(n) }
func BuildIndex(docs []Doc) *Index          { return cluster.BuildIndex(docs) }
func NewEngine(idx *Index) *Engine          { return cluster.NewEngine(idx) }
func OpenStore(path string) (*Store, error) { return cluster.OpenStore(path) }
func OpenStoreWithOptions(db *sql.DB, opts StoreOptions) (*Store, error) {
	return cluster.OpenStoreWithOptions(db, opts)
}
func OpenDualClusterStore(cipherDB, plainDB *sql.DB) (*DualClusterStore, error) {
	return cluster.OpenDualClusterStore(cipherDB, plainDB)
}
func ComputeCorpusDigest(idx *Index) uint64 { return cluster.ComputeCorpusDigest(idx) }
func WriteDiskClusterCSR(path string, eng *Engine) error {
	return cluster.WriteDiskClusterCSR(path, eng)
}
func OpenDiskClusterEngine(path string) (*Engine, error) {
	return cluster.OpenDiskClusterEngine(path)
}
func AttachDiskClusterCSR(path string, eng *Engine, releaseHeapDuplicates bool) error {
	return cluster.AttachDiskClusterCSR(path, eng, releaseHeapDuplicates)
}
func FormatPlaintextWithMeta(rawText string, meta UnitMeta) string {
	return codec.FormatPlaintextWithMeta(rawText, meta)
}
func CanonicalizeApostrophe(s string) string { return codec.CanonicalizeApostrophe(s) }
func IsContinuousSyllabicOrIdeographic(r rune) bool {
	return codec.IsContinuousSyllabicOrIdeographic(r)
}
func NewBlindEncoder(secret []byte) *BlindEncoder { return codec.NewBlindEncoder(secret) }
func NewBlindEncoderFromRawKey(rawKey [32]byte) *BlindEncoder {
	return codec.NewBlindEncoderFromRawKey(rawKey)
}
func CarrierUnits(r rune) uint16          { return codec.CarrierUnits(r) }
func SplitSentences(text string) []string { return codec.SplitSentences(text) }
func SplitWords(sent string) []string     { return codec.SplitWords(sent) }
func ExtractStructuredDateHarmonics(sent string) []string {
	return codec.ExtractStructuredDateHarmonics(sent)
}
func ComposeSubUnitID(parentID, kind string, index int) string {
	return codec.ComposeSubUnitID(parentID, kind, index)
}
func BlindFormFlagMarks(line string, words []string) []uint8 {
	return codec.BlindFormFlagMarks(line, words)
}
func LookupISO3166GeoHarmonicPair(token string) (string, string) {
	return codec.LookupISO3166GeoHarmonicPair(token)
}
func LookupISO3166GeoHarmonics(token string) []string {
	return codec.LookupISO3166GeoHarmonics(token)
}
func LookupUnitDimensionHarmonicPair(token string) (string, string) {
	return codec.LookupUnitDimensionHarmonicPair(token)
}
func LookupClosedSetHarmonics(token string) []string {
	return codec.LookupClosedSetHarmonics(token)
}
func CombineHarmonicExpanders(es ...HarmonicExpander) HarmonicExpander {
	return codec.CombineHarmonicExpanders(es...)
}
func InverseSplit(slots []TermSample) []TermSample  { return codec.InverseSplit(slots) }
func FoldSamples(samples []TermSample) []FoldedTerm { return codec.FoldSamples(samples) }
func ExtractTerms(slots []TermSample) []FoldedTerm  { return codec.ExtractTerms(slots) }
func HashPhraseDyad(left, right uint64) uint64      { return codec.HashPhraseDyad(left, right) }
func ExtractAdjacentPhrases(frames [][]TermSample) []PhraseDyad {
	return codec.ExtractAdjacentPhrases(frames)
}
func ExtractCrestPhrases(frames [][]TermSample) []CrestPhrase {
	return codec.ExtractCrestPhrases(frames)
}
func ExtractPulsePrimaryDyads(frames [][]PulseSlot) []PhraseDyad {
	return codec.ExtractPulsePrimaryDyads(frames)
}
func ExtractDecorationClosedSet(low string) []string {
	return codec.ExtractDecorationClosedSet(low)
}
func ExtractMorphShaping(low string) []string       { return codec.ExtractMorphShaping(low) }
func CanonicalCompanionTags(token string) []string  { return codec.CanonicalCompanionTags(token) }
func NewTermExtractor(secret []byte) *TermExtractor { return codec.NewTermExtractor(secret) }
func NewBaselineTermExtractor(secret []byte) *TermExtractor {
	return codec.NewBaselineTermExtractor(secret)
}

func FuseSpaceElision(sentence string) SpaceElision { return codec.FuseSpaceElision(sentence) }
func CanonicalPhrase(parts ...string) string        { return codec.CanonicalPhrase(parts...) }
