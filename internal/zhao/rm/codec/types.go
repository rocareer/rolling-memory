// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package codec 维护 Zhao 架构的脉冲编码器、8-bit 结构标志位、词元装饰、词项抽取器与空格粘合器。
package codec

import "github.com/rocareer/rolling-memory/internal/zhao/rm/cluster"

type (
	TermRole     = cluster.TermRole
	Term         = cluster.Term
	TermPosition = cluster.TermPosition
	DocMeta      = cluster.DocMeta
	Doc          = cluster.Doc
	Index        = cluster.Index
)

const (
	RolePrimary            = cluster.RolePrimary
	RoleMorphStem          = cluster.RoleMorphStem
	RoleSubwordPiece       = cluster.RoleSubwordPiece
	RoleClosedBeacon       = cluster.RoleClosedBeacon
	RoleBiwordHomomorph    = cluster.RoleBiwordHomomorph
	ExtFlagSeenPrimary     = cluster.ExtFlagSeenPrimary
	ExtFlagSeenMorphOrClos = cluster.ExtFlagSeenMorphOrClos
	ExtFlagSeenSubword     = cluster.ExtFlagSeenSubword
)

// PulseSlot is the fixed-width binary carrier slot record {H, RawBitsX10, Flags}.
type PulseSlot struct {
	H          uint64
	RawBitsX10 uint16
	Flags      uint8
}
