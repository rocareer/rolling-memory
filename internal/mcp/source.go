// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"errors"
	"io"
	"strings"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

func sourceUnitArgument(arguments map[string]any) (string, error) {
	value, present := arguments["unit_id"]
	if !present {
		return "", nil
	}
	if len(arguments) != 1 {
		return "", errors.New("send only unit_id to read a cited conversation source")
	}
	id, _ := value.(string)
	digest, ok := strings.CutPrefix(id, "ep_")
	if !ok || len(digest) != 24 || strings.Trim(digest, "0123456789abcdef") != "" {
		return "", errors.New("unit_id must be a cited conversation source ID")
	}
	return id, nil
}

func sourceUnitResponse(requestID any, memory *facade.Memory, id string, debugOut io.Writer) any {
	client, err := memory.LocalVault()
	if err != nil {
		return toolError(requestID, err, debugOut)
	}
	body, err := client.Vault.LoadEpisodicSource(id)
	if err != nil || strings.TrimSpace(body) == "" || episodicunit.DocID(strings.TrimSpace(memory.Config.Scope), body) != id {
		return toolError(requestID, errors.New("conversation source unavailable for "+id), debugOut)
	}
	// Allow room for JSON escaping and the relay envelope; never shorten an exact read.
	if len(body) > maxHTTPBody/8 {
		return toolError(requestID, errors.New("conversation source is too large for one MCP response"), debugOut)
	}
	return resultResponse(requestID, map[string]any{
		"content": []any{map[string]any{
			"type": "text",
			"text": "Conversation source unit " + id + " (one unit, not the full turn):\n\n" + body,
		}},
		"isError": false,
	})
}
