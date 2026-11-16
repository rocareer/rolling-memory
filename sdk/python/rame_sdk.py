#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
# Author: Albert <albert@rocareer.com>

"""
RAME Official Python Client SDK (RAME-BlindPulse-v1 Standard)
=============================================================
100% Vector-Free · 100% LLM-Free · Irreversible Blind Integer Pulses `{h, b, f}`
Zero third-party dependencies (Python 3 standard library only).
Bit-for-bit identical with Go (`sdk/go`), Node.js (`sdk/nodejs`), and Java (`sdk/java`) SDKs.
"""

from __future__ import annotations

import base64
import bisect
import datetime
import hashlib
import hmac
import json
import os
import struct
import sys
import unicodedata
import urllib.parse
import urllib.request
from typing import Any, Callable, Dict, List, Optional, Protocol, Tuple

SPEC_VERSION = "RAME-BlindPulse-v1"
SDK_VERSION = "1.11.3"

MODE_LOCAL_PLAIN = "local_plain"
MODE_PULSE_PLAIN = "pulse_plain"
MODE_PULSE_CIPHER = "pulse_cipher"

FLAG_HAS_UPPER = 1 << 0
FLAG_HAS_CAMEL = 1 << 1
FLAG_HAS_DIGIT = 1 << 2
FLAG_HAS_CONN = 1 << 3
# FlagIsHarmonicCompanion (bit4) marks a companion harmonic overtone slot (stem / root / unit /
# dimension): the explicit twin of the implicit odd-parity carrier channel.
FLAG_IS_HARMONIC_COMPANION = 1 << 4
# Declared high-nibble form flags (bit5/bit6/bit7): pure-Unicode structural facts that are free on
# the client plaintext side and impossible to rebuild from blind {h, b, f} pulses. They feed
# continuous gains and soft suppression only — never a boolean gate.
FLAG_IS_FOCAL_HEADER = 1 << 5
FLAG_HAS_BOUNDARY_QUOTE = 1 << 6
FLAG_IS_QUANTITY_OR_UNIT = 1 << 7

# CALENDAR_BEACON_WIDTH_X10 is the carrier width of the canonical calendar-beacon companion pulse
# (`H(K_user, "cal:"+iso)`, `f = FLAG_HAS_DIGIT | FLAG_HAS_CONN`) emitted alongside the ISO date
# pulse when structured dates are enabled. The dedicated width (distinct from the native
# date-token width 284) marks the pulse as an opaque calendar anchor the cloud engine consumes
# for calendar-axis resonance; every shipped SDK and companion packer pins this exact value.
CALENDAR_BEACON_WIDTH_X10 = 588

CANARY_SECRET = "rame-codec-canary-key-v1"
CANARY_DOC_ID = "CANARY_V1"
CANARY_TEXT = "RollingMemory v1.0.1 WaveEngine_256 洛灵记忆. Zero plaintext!"
CANARY_TS = 1700000000
CANARY_SLOT_COUNT = 21
CANARY_SENT_COUNT = 2
CANARY_ATOMS_B64 = (
    "Uk1CUAEAPA0AFAAAABUAAhQhUFBjZHiLAYwBtAHIAYQCtQJAEATBATAPB0jHISlR96QHZAI40Tt3T0fPAxxcTN+orGsN87QYgKG+"
    "M3wAaBUxyxkJx8f8zRbMt024NoH4VZEtimANzWUASZ6GWiX3Z7rb64Eq7inVwwMYS98I2I7VbHWwUFW4WszbtBQnTCgoPkEZy9pj"
    "GebXDmmqB9vwbo0DeZvaEa9TH6Ti6fHUlg3MbBKkCYTXBbqHJcBSS+ORCU+K9G0qrLWQNLboQC4oYxzk6gIUARCElBIB"
)

_SENTENCE_BOUNDS = frozenset("\n\r。！？；;!?")
_WORD_CONNS = frozenset("-_.")


def is_space(ch: str) -> bool:
    return ch in " \t\n\v\f\r\x85" or unicodedata.category(ch) in ("Zs", "Zl", "Zp")


def trim_space(s: str) -> str:
    if not s:
        return ""
    n = len(s)
    start = 0
    while start < n and is_space(s[start]):
        start += 1
    end = n
    while end > start and is_space(s[end - 1]):
        end -= 1
    return s[start:end]


def simple_lower_cp(ch: str) -> str:
    return "i" if ord(ch) == 0x0130 else ch.lower()


def simple_lower(s: str) -> str:
    return "".join(simple_lower_cp(ch) for ch in s)


def format_plaintext_with_meta(
    raw_text: str,
    author: str = "",
    occurred_at: int = 0,
    narrated_at: int = 0,
    ingested_at: int = 0,
    file_parse: str = "",
) -> str:
    text = trim_space(raw_text or "")
    fp = trim_space(file_parse or "")
    if not text and fp:
        text = fp
        raw_text = fp
    elif not text:
        return raw_text
    elif fp and fp not in text:
        text = text + "\n" + fp
        raw_text = text
    auth = trim_space(author or "")
    if not auth and narrated_at <= 0 and occurred_at <= 0 and ingested_at <= 0:
        return raw_text

    def fmt_day(ts: int) -> str:
        if ts <= 0:
            return ""
        return datetime.datetime.fromtimestamp(ts, tz=datetime.timezone.utc).strftime("%Y-%m-%d")

    occ_day, nar_day, ing_day = fmt_day(occurred_at), fmt_day(narrated_at), fmt_day(ingested_at)
    primary_day = occ_day or nar_day
    time_parts: List[str] = []
    if primary_day:
        time_parts.append(primary_day)
    if nar_day and nar_day != primary_day:
        time_parts.append("narrated:" + nar_day)
    if ing_day and ing_day != primary_day and ing_day != nar_day:
        time_parts.append("ingested:" + ing_day)
    time_tag = f"[{'|'.join(time_parts)}]" if time_parts else ""
    if time_tag and auth:
        prefix = f"{time_tag} {auth}: "
    elif time_tag:
        prefix = f"{time_tag} "
    elif auth:
        prefix = f"{auth}: "
    else:
        prefix = ""
    if not prefix or text.startswith(prefix):
        return text
    return prefix + text


def compose_sub_unit_id(parent_id: str, kind: str, index: int) -> str:
    """Return the canonical sub-unit id: ``<parent>#<kind>`` / ``<parent>#<kind>:<index>``."""
    parent = trim_space(parent_id or "")
    k = trim_space(kind or "") or "sub"
    if index <= 0:
        return f"{parent}#{k}"
    return f"{parent}#{k}:{index}"


class SubUnit:
    """One high-resolution free block inside a parent unit (4D spatial addressing).

    The block carries its own logical block id and 1-based line span in the source
    document; ``pack_composite_packets`` encodes those into ``block_h`` /
    ``line_start`` / ``line_end`` and cites the parent unit id.
    """

    __slots__ = ("kind", "id", "text", "block_id", "file_parse", "line_start", "line_end")

    def __init__(
        self,
        kind: str = "",
        text: str = "",
        id: str = "",
        block_id: str = "",
        file_parse: str = "",
        line_start: int = 0,
        line_end: int = 0,
    ) -> None:
        self.kind = kind or ""
        self.id = id or ""
        self.text = text or ""
        self.block_id = block_id or ""
        self.file_parse = file_parse or ""
        self.line_start = int(line_start or 0)
        self.line_end = int(line_end or 0)

    @classmethod
    def from_mapping(cls, value: Any) -> "SubUnit":
        if isinstance(value, SubUnit):
            return value
        if not isinstance(value, dict):
            raise TypeError("SubUnit must be a mapping or SubUnit instance")
        return cls(
            kind=value.get("kind", ""),
            text=value.get("text", ""),
            id=value.get("id", ""),
            block_id=value.get("block_id", value.get("blockId", "")),
            file_parse=value.get("file_parse", value.get("fileParse", "")),
            line_start=value.get("line_start", value.get("lineStart", 0)),
            line_end=value.get("line_end", value.get("lineEnd", 0)),
        )


def is_han_cp(cp: int) -> bool:
    return (0x4E00 <= cp <= 0x9FFF) or (0x3400 <= cp <= 0x4DBF) or (0xF900 <= cp <= 0xFAFF) or (0x20000 <= cp <= 0x2FA1F)


def is_hangul_cp(cp: int) -> bool:
    return (0xAC00 <= cp <= 0xD7AF) or (0x1100 <= cp <= 0x11FF) or (0x3130 <= cp <= 0x318F)


def is_kana_or_se_asian_cp(cp: int) -> bool:
    return cp == 0x30FC or (0x3040 <= cp <= 0x30FF) or (0xFF66 <= cp <= 0xFF9F) or (0x0E00 <= cp <= 0x0EFF) or (0x1780 <= cp <= 0x17FF)


def is_unspaced_ideographic(ch: str) -> bool:
    cp = ord(ch)
    return is_han_cp(cp) or is_kana_or_se_asian_cp(cp)


def is_letter(ch: str) -> bool:
    return unicodedata.category(ch)[0] == "L"


def is_number(ch: str) -> bool:
    return unicodedata.category(ch)[0] == "N"


def is_digit(ch: str) -> bool:
    return unicodedata.category(ch) == "Nd"


def is_upper(ch: str) -> bool:
    return unicodedata.category(ch) == "Lu"


def is_lower(ch: str) -> bool:
    return unicodedata.category(ch) == "Ll"


def is_letter_or_number(ch: str) -> bool:
    return unicodedata.category(ch)[0] in ("L", "N", "M")


def split_compound_subwords(tok: str) -> List[str]:
    parts: List[str] = []
    cur: List[str] = []
    prev_r: Optional[str] = None
    for idx, r in enumerate(tok):
        if r in _WORD_CONNS:
            if cur:
                parts.append("".join(cur))
                cur.clear()
            prev_r = None
            continue
        next_is_lower = False
        if idx + 1 < len(tok):
            if is_lower(tok[idx + 1]):
                next_is_lower = True
        
        if prev_r is not None and cur and ((is_lower(prev_r) and is_upper(r)) or 
                                           (is_upper(prev_r) and is_upper(r) and next_is_lower) or 
                                           (is_number(prev_r) != is_number(r))):
            parts.append("".join(cur))
            cur.clear()
        cur.append(r)
        prev_r = r
    if cur:
        parts.append("".join(cur))
    return parts if len(parts) >= 2 else []


def carrier_units(ch: str) -> int:
    cp = ord(ch)
    if cp < 0x80:
        if (0x61 <= cp <= 0x7A) or (0x41 <= cp <= 0x5A):
            return 20
        return 33 if (0x30 <= cp <= 0x39) else 10
    if is_han_cp(cp):
        return 100
    if is_hangul_cp(cp):
        return 70
    if is_kana_or_se_asian_cp(cp):
        return 42
    if is_digit(ch):
        return 33
    return 20 if is_letter(ch) else 10


def _classify_flag(r: str, upper_ok: bool, has_lower: bool) -> Tuple[int, bool]:
    if is_upper(r):
        return (FLAG_HAS_UPPER if upper_ok else 0) | (FLAG_HAS_CAMEL if has_lower else 0), has_lower
    if is_lower(r):
        return 0, True
    if is_number(r):
        return FLAG_HAS_DIGIT, has_lower
    return (FLAG_HAS_CONN if r in _WORD_CONNS else 0), has_lower


def split_sentences(text: str) -> List[str]:
    if not text or not trim_space(text):
        return []
    out: List[str] = []
    cur: List[str] = []
    n = len(text)
    for i, ch in enumerate(text):
        is_bound = ch in _SENTENCE_BOUNDS or (
            ch == "." and not ((i > 0 and is_letter_or_number(text[i - 1])) and (i + 1 < n and is_letter_or_number(text[i + 1])))
        )
        if is_bound:
            s = trim_space("".join(cur))
            if s:
                out.append(s)
            cur.clear()
        else:
            cur.append(ch)
    tail = trim_space("".join(cur))
    if tail:
        out.append(tail)
    return out


def split_words(sent: str) -> List[str]:
    if not sent or not trim_space(sent):
        return []
    units: List[str] = []
    i, n = 0, len(sent)
    while i < n:
        ch = sent[i]
        if not is_letter_or_number(ch):
            i += 1
            continue
        if is_unspaced_ideographic(ch):
            if is_han_cp(ord(ch)):
                end = i
                while end < n and is_han_cp(ord(sent[end])):
                    end += 1
                if end - i == 1:
                    units.append(sent[i:end])
                else:
                    units.extend(sent[k : k + 2] for k in range(i, end - 1))
                    units.extend(sent[k : k + 1] for k in range(i, end))
                i = end
                continue
            end = i + 1
            while end < n and is_unspaced_ideographic(sent[end]) and not is_han_cp(ord(sent[end])):
                end += 1
            units.append(sent[i:end])
            i = end
            continue

        end = i + 1
        while end < n:
            nch = sent[end]
            if is_letter_or_number(nch) and not is_unspaced_ideographic(nch):
                end += 1
            elif nch in _WORD_CONNS and end + 1 < n and is_letter_or_number(sent[end + 1]) and not is_unspaced_ideographic(sent[end + 1]):
                end += 1
            else:
                break
        tok = sent[i:end]
        units.append(tok)
        subs = split_compound_subwords(tok)
        if len(subs) >= 2:
            units.extend(subs)
        i = end
    return units


# ============================================================================
# Declared high-nibble form flags (bit5 focal header / bit6 boundary quote / bit7 quantity dyad)
# Pure-Unicode structural facts, zero lexicon: they only use Unicode general categories
# (Ps/Pe/Pi/Pf), the CommonMark ATX heading bound ('#' 1..6) and the connector / compound-splitting
# predicates shared with the tokenizer. They drive continuous gains and soft suppression only.
# ============================================================================


def _first_non_space_char(line: str) -> str:
    for ch in line:
        if not is_space(ch):
            return ch
    return ""


def _is_strong_delimiter(ch: str) -> bool:
    return bool(ch) and unicodedata.category(ch) in ("Ps", "Pe", "Pi", "Pf")


def _is_field_label_colon(ch: str) -> bool:
    return ch in (":", "：")


def _is_blind_sentence_terminator(ch: str) -> bool:
    return ch in ("\n", "\r", ".", "。", "！", "？", "；", ";", "!", "?")


def _is_blind_numeric_only_token(tok: str) -> bool:
    has_digit = False
    for ch in tok:
        if is_number(ch):
            has_digit = True
        elif ch in _WORD_CONNS:
            continue
        else:
            return False
    return has_digit


def _is_blind_letter_only_token(tok: str) -> bool:
    has_letter = False
    for ch in tok:
        if is_number(ch):
            return False
        if is_letter(ch) or unicodedata.category(ch)[0] == "M":
            has_letter = True
    return has_letter


def _is_blind_digit_led_alnum_token(tok: str) -> bool:
    if not tok or not is_number(tok[0]):
        return False
    return not _is_blind_numeric_only_token(tok)


def _blind_compound_parent_idx(words: List[str]) -> List[int]:
    """Rebuild the compound-splitting structure of split_words (children -> parent index, else -1)."""
    parents = [-1] * len(words)
    for i in range(len(words)):
        if parents[i] >= 0:
            continue
        subs = split_compound_subwords(words[i])
        if len(subs) < 2:
            continue
        for k in range(1, len(subs) + 1):
            if i + k >= len(words) or words[i + k] != subs[k - 1]:
                break
            parents[i + k] = i
    return parents


def _blind_numeric_anchor_at(words: List[str], i: int) -> bool:
    return 0 <= i < len(words) and _is_blind_numeric_only_token(words[i])


def _blind_unit_carrier_at(words: List[str], parents: List[int], anchor: int, i: int) -> bool:
    """Dim side of a dyad: glued children of a digit-led parent agree; standalone items need a
    pure numeric anchor parent (digit children of letter-led parents such as v1.0.1 never leak)."""
    if not _is_blind_letter_only_token(words[i]):
        return False
    carrier_parent = parents[i]
    if carrier_parent >= 0:
        return carrier_parent == parents[anchor] and _is_blind_digit_led_alnum_token(words[carrier_parent])
    anchor_parent = parents[anchor]
    if anchor_parent >= 0:
        return _is_blind_numeric_only_token(words[anchor_parent])
    return True


def _blind_adjacent_carrier_starts(line: str) -> List[int]:
    """Char offsets of every word run that directly follows a pure numeric run with only
    whitespace between (a punctuation separator such as `10 & x` never forms a dyad)."""
    starts: List[int] = []
    i, n = 0, len(line)
    while i < n:
        if not is_number(line[i]) and line[i] not in _WORD_CONNS:
            i += 1
            continue
        has_digit = False
        j = i
        while j < n:
            if is_number(line[j]):
                has_digit = True
                j += 1
                continue
            if line[j] in _WORD_CONNS:
                j += 1
                continue
            break
        if has_digit:
            k = j
            while k < n and is_space(line[k]):
                k += 1
            if k < n and (is_letter(line[k]) or unicodedata.category(line[k])[0] == "M"):
                starts.append(k)
        i = j
    return starts


def _blind_carrier_textually_adjacent(line: str, starts: List[int], carrier: str) -> bool:
    if not carrier:
        return False
    return any(line[s:].startswith(carrier) for s in starts)


def _is_atx_heading_prefix(trimmed: str) -> bool:
    n = 0
    for ch in trimmed:
        if ch != "#":
            break
        n += 1
        if n > 6:
            return False
    if n == 0:
        return False
    rest = trimmed[n:]
    return rest == "" or not rest.startswith("#")


def _is_blind_numeric_led_value(rest: str) -> bool:
    pos = 0
    while pos < len(rest) and is_space(rest[pos]):
        pos += 1
    rest = rest[pos:]
    if not rest:
        return False
    has_digit = False
    for ch in rest:
        if is_number(ch):
            has_digit = True
        elif ch in _WORD_CONNS:
            continue
        else:
            return has_digit
    return has_digit


def _has_leading_field_label_prefix(trimmed: str) -> bool:
    colon = -1
    for idx, ch in enumerate(trimmed):
        if _is_field_label_colon(ch):
            colon = idx
            break
    if colon <= 0:
        return False
    for ch in trimmed[:colon]:
        if is_space(ch) or _is_blind_sentence_terminator(ch):
            return False
    return _is_blind_numeric_led_value(trimmed[colon + 1:])


def _is_blind_focal_header_line(line: str) -> bool:
    trimmed = trim_space(line)
    if not trimmed:
        return False
    if _is_atx_heading_prefix(trimmed):
        return True
    return _has_leading_field_label_prefix(trimmed)


def blind_form_flag_marks(line: str, words: List[str]) -> List[int]:
    """Declared form flags aligned one-to-one with the split_words items of the sentence."""
    if not words:
        return []
    marks = [0] * len(words)
    first = _first_non_space_char(line)
    if first and _is_strong_delimiter(first):
        marks[0] |= FLAG_HAS_BOUNDARY_QUOTE
    if _is_blind_focal_header_line(line):
        marks[0] |= FLAG_IS_FOCAL_HEADER
    parents = _blind_compound_parent_idx(words)
    starts = _blind_adjacent_carrier_starts(line)
    for i in range(1, len(words)):
        if not _blind_numeric_anchor_at(words, i - 1) or not _blind_unit_carrier_at(words, parents, i - 1, i):
            continue
        if not _blind_carrier_textually_adjacent(line, starts, words[i]):
            continue
        marks[i] |= FLAG_IS_QUANTITY_OR_UNIT
    return marks


# ============================================================================
# Canonical RAME-BlindPulse-v1 Binary Wire Codec ("RMBP" v1 · Mixed-Radix Golomb-Rice)
# ============================================================================

_WIRE_MAGIC = b"RMBP"
_WIRE_VERSION = 1
_WIRE_HEADER_BYTES = 16
_WIRE_MAX_F = 0xFF
_WIRE_MAX_DICT_N = 65535
_WIRE_MAX_SLOT_N = 1 << 20
_WIRE_MAX_SENT_N = 65535
_WIRE_MAX_PAL_N = 255
_WIRE_FLAG_IDENTITY_INDEX = 1 << 0
_WIRE_FLAG_WIDE_F = 1 << 1
_WIRE_MAX_FLAG = _WIRE_FLAG_IDENTITY_INDEX | _WIRE_FLAG_WIDE_F


def _golomb_k_for_dict(dict_n: int) -> int:
    if dict_n <= 0:
        return 0
    if dict_n == 1:
        return 64
    lg = dict_n.bit_length() - 1
    return max(48, min(64, 64 - lg))


def _bits_for_dict(dict_n: int) -> int:
    if dict_n <= 1:
        return 0
    return (dict_n - 1).bit_length()


def _append_varint(out: bytearray, v: int) -> None:
    while v >= 0x80:
        out.append((v & 0x7F) | 0x80)
        v >>= 7
    out.append(v & 0x7F)


def _varint_len(v: int) -> int:
    if v < 0x80:
        return 1
    if v < 0x4000:
        return 2
    return 3


def _varint32_len(v: int) -> int:
    if v < (1 << 7):
        return 1
    if v < (1 << 14):
        return 2
    if v < (1 << 21):
        return 3
    if v < (1 << 28):
        return 4
    return 5


def _read_varint(raw: bytes, off: int) -> Tuple[int, int]:
    out = 0
    shift = 0
    n = 1
    while True:
        if off + n - 1 >= len(raw):
            raise ValueError("ramesdk: wire varint truncated")
        if n > 3:
            raise ValueError("ramesdk: wire varint overflow")
        c = raw[off + n - 1]
        out |= (c & 0x7F) << shift
        if (c & 0x80) == 0:
            if out > 0xFFFF or _varint_len(out) != n:
                raise ValueError("ramesdk: wire varint non-canonical")
            return out, n
        shift += 7
        n += 1


def _read_varint32(raw: bytes, off: int) -> Tuple[int, int]:
    out = 0
    shift = 0
    n = 1
    while True:
        if off + n - 1 >= len(raw):
            raise ValueError("ramesdk: wire varint32 truncated")
        if n > 5:
            raise ValueError("ramesdk: wire varint32 overflow")
        c = raw[off + n - 1]
        out |= (c & 0x7F) << shift
        if (c & 0x80) == 0:
            if out > 0xFFFFFFFF or _varint32_len(out) != n:
                raise ValueError("ramesdk: wire varint32 non-canonical")
            return out, n
        shift += 7
        n += 1


class _BitWriter:
    def __init__(self) -> None:
        self.buf = bytearray()
        self.cur = 0
        self.n = 0

    def write(self, v: int, width: int) -> None:
        for i in range(width - 1, -1, -1):
            self.cur = ((self.cur << 1) | ((v >> i) & 1)) & 0xFF
            self.n += 1
            if self.n == 8:
                self.buf.append(self.cur)
                self.cur = 0
                self.n = 0

    def flush(self) -> None:
        if self.n > 0:
            self.buf.append((self.cur << (8 - self.n)) & 0xFF)
            self.cur = 0
            self.n = 0


class _BitReader:
    def __init__(self, raw: bytes) -> None:
        self.raw = raw
        self.bit = 0

    def read(self, width: int) -> int:
        out = 0
        total_bits = len(self.raw) * 8
        for _ in range(width):
            if self.bit >= total_bits:
                raise ValueError("ramesdk: wire bitstream truncated")
            byte_idx = self.bit // 8
            bit_idx = 7 - (self.bit % 8)
            out = (out << 1) | ((self.raw[byte_idx] >> bit_idx) & 1)
            self.bit += 1
        return out

    def bytes_read(self) -> int:
        return (self.bit + 7) // 8

    def verify_zero_padding(self) -> None:
        rem = self.bit % 8
        if rem == 0:
            return
        mask = (1 << (8 - rem)) - 1
        if (self.raw[self.bit // 8] & mask) != 0:
            raise ValueError("ramesdk: wire bitstream non-zero padding")


def encode_wire_bytes(
    slots: List[Dict[str, int]],
    frames: Optional[List[List[Dict[str, int]]]] = None,
) -> bytes:
    sent: List[int] = []
    if frames:
        cum = 0
        for fr in frames:
            cum += len(fr)
            sent.append(cum)
        if not slots:
            slots = [a for fr in frames for a in fr]
        elif cum != len(slots):
            raise ValueError(f"ramesdk: sentence frames hold {cum} atoms, slots hold {len(slots)}")
    if len(slots) > _WIRE_MAX_SLOT_N:
        raise ValueError(f"ramesdk: slotN={len(slots)} exceeds capacity")
    if len(sent) > _WIRE_MAX_SENT_N:
        raise ValueError(f"ramesdk: sentN={len(sent)} exceeds capacity")

    atom_tuples: List[Tuple[int, int, int]] = []
    for a in slots:
        h, b, f = int(a["h"]), int(a["b"]), int(a["f"])
        if h < 0 or h > 0xFFFFFFFFFFFFFFFF:
            raise ValueError(f"ramesdk: atom h {h:#x} out of uint64 range")
        if b < 0 or b > 0xFFFF:
            raise ValueError(f"ramesdk: atom b {b} out of uint16 range")
        if f < 0 or f > _WIRE_MAX_F:
            raise ValueError(f"ramesdk: atom flag {f:#x} out of 8-bit range (0x00..0xFF)")
        atom_tuples.append((h, b, f))

    prev = 0
    for o in sent:
        if o <= prev or o > len(atom_tuples):
            raise ValueError("ramesdk: invalid sentence offsets")
        prev = o
    if sent and prev != len(atom_tuples):
        raise ValueError("ramesdk: last sentence offset != slotN")

    unique_set = set(atom_tuples)
    if len(unique_set) >= _WIRE_MAX_DICT_N:
        raise ValueError(f"ramesdk: dictN exceeds {_WIRE_MAX_DICT_N}")
    dict_list = sorted(unique_set)
    dict_pos = {t: i for i, t in enumerate(dict_list)}

    index: List[int] = []
    identity = len(dict_list) == len(atom_tuples)
    for i, t in enumerate(atom_tuples):
        p = dict_pos[t]
        index.append(p)
        if p != i:
            identity = False

    gr_k = _golomb_k_for_dict(len(dict_list))

    bf_set = {(t[1], t[2]) for t in dict_list}
    use_palette = len(dict_list) > 0 and len(bf_set) <= _WIRE_MAX_PAL_N
    palette = sorted(bf_set) if use_palette else []
    pal_pos = {bf: i for i, bf in enumerate(palette)} if use_palette else {}
    pal_n = len(palette) if use_palette else 0

    flags = _WIRE_FLAG_IDENTITY_INDEX if identity else 0
    wide_f = any(t[2] > 0x0F for t in dict_list)
    if wide_f:
        flags |= _WIRE_FLAG_WIDE_F
    out = bytearray(_WIRE_MAGIC)
    out.extend((_WIRE_VERSION, flags, gr_k, pal_n))
    out.extend(struct.pack(">HIH", len(dict_list), len(atom_tuples), len(sent)))

    if use_palette:
        for b_val, _ in palette:
            _append_varint(out, b_val)
        if wide_f:
            for _, f_val in palette:
                out.append(f_val)
        else:
            f_col = bytearray((pal_n + 1) // 2)
            for i, (_, f_val) in enumerate(palette):
                if i % 2 == 0:
                    f_col[i // 2] = f_val
                else:
                    f_col[i // 2] |= f_val << 4
            out.extend(f_col)
    elif dict_list:
        for _, b_val, _ in dict_list:
            _append_varint(out, b_val)
        if wide_f:
            for _, _, f_val in dict_list:
                out.append(f_val)
        else:
            f_col = bytearray((len(dict_list) + 1) // 2)
            for i, (_, _, f_val) in enumerate(dict_list):
                if i % 2 == 0:
                    f_col[i // 2] = f_val
                else:
                    f_col[i // 2] |= f_val << 4
            out.extend(f_col)

    bw = _BitWriter()
    prev_h = 0
    mask_k = (1 << gr_k) - 1 if gr_k < 64 else 0xFFFFFFFFFFFFFFFF
    for i, (h_val, _, _) in enumerate(dict_list):
        delta = h_val if i == 0 else (h_val - prev_h)
        prev_h = h_val
        if gr_k == 64:
            bw.write(delta, 64)
        else:
            q = delta >> gr_k
            rem = delta & mask_k
            for _ in range(q):
                bw.write(1, 1)
            bw.write(0, 1)
            bw.write(rem, gr_k)

    if use_palette:
        pal_bits = _bits_for_dict(pal_n)
        if pal_bits > 0:
            for _, b_val, f_val in dict_list:
                bw.write(pal_pos[(b_val, f_val)], pal_bits)

    if not identity and len(dict_list) > 1:
        seen_pos = [-1] * len(dict_list)
        remaining = list(range(len(dict_list)))
        seen_count = 0
        for d_idx in index:
            if seen_count == 0:
                r_pos = bisect.bisect_left(remaining, d_idx)
                rem_bits = _bits_for_dict(len(remaining))
                bw.write(r_pos, rem_bits)
                remaining.pop(r_pos)
                seen_pos[d_idx] = seen_count
                seen_count += 1
                continue
            sp = seen_pos[d_idx]
            if sp >= 0:
                if remaining:
                    bw.write(1, 1)
                s_bits = _bits_for_dict(seen_count)
                if s_bits > 0:
                    bw.write(sp, s_bits)
            else:
                bw.write(0, 1)
                r_pos = bisect.bisect_left(remaining, d_idx)
                rem_bits = _bits_for_dict(len(remaining))
                if rem_bits > 0:
                    bw.write(r_pos, rem_bits)
                remaining.pop(r_pos)
                seen_pos[d_idx] = seen_count
                seen_count += 1

    bw.flush()
    out.extend(bw.buf)

    prev_off = 0
    for o in sent:
        _append_varint(out, o - prev_off - 1)
        prev_off = o
    return bytes(out)


def decode_wire_bytes(raw: bytes) -> Tuple[List[Dict[str, int]], List[List[Dict[str, int]]]]:
    if len(raw) < _WIRE_HEADER_BYTES:
        raise ValueError("ramesdk: wire payload truncated")
    if raw[:4] != _WIRE_MAGIC:
        raise ValueError("ramesdk: wire payload magic mismatch")
    if raw[4] != _WIRE_VERSION:
        raise ValueError(f"ramesdk: wire payload unsupported version {raw[4]}")
    flags, gr_k, pal_n = raw[5], raw[6], raw[7]
    if flags & ~_WIRE_MAX_FLAG:
        raise ValueError("ramesdk: wire payload invalid flags")
    dict_n, slot_n, sent_n = struct.unpack(">HIH", raw[8:16])
    if dict_n != 0 and gr_k != _golomb_k_for_dict(dict_n):
        raise ValueError(f"ramesdk: wire payload grK={gr_k} want {_golomb_k_for_dict(dict_n)}")
    if dict_n == 0 and (gr_k != 0 or pal_n != 0 or slot_n != 0):
        raise ValueError("ramesdk: wire payload invalid empty header")
    if pal_n > dict_n:
        raise ValueError("ramesdk: wire payload palN > dictN")
    identity = (flags & _WIRE_FLAG_IDENTITY_INDEX) != 0
    wide_f = (flags & _WIRE_FLAG_WIDE_F) != 0
    if identity and dict_n != slot_n:
        raise ValueError("ramesdk: wire payload identity mismatch")
    if dict_n == 0 and wide_f:
        raise ValueError("ramesdk: wire payload wide-f header without dictionary")
    if slot_n > _WIRE_MAX_SLOT_N or (dict_n > 1 and slot_n > len(raw) * 8):
        raise ValueError(f"ramesdk: wire payload slotN={slot_n} exceeds capacity")

    off = _WIRE_HEADER_BYTES
    dict_b = [0] * dict_n
    dict_f = [0] * dict_n
    palette: List[Tuple[int, int]] = []

    if pal_n > 0:
        pal_b = [0] * pal_n
        for i in range(pal_n):
            pal_b[i], n = _read_varint(raw, off)
            off += n
        if wide_f:
            if off + pal_n > len(raw):
                raise ValueError("ramesdk: wire payload truncated in palette")
            has_high = False
            for i in range(pal_n):
                palette.append((pal_b[i], raw[off + i]))
                if raw[off + i] > 0x0F:
                    has_high = True
            if not has_high:
                raise ValueError("ramesdk: wire payload wide-f palette without high nibble")
            off += pal_n
        else:
            f_bytes = (pal_n + 1) // 2
            if off + f_bytes > len(raw):
                raise ValueError("ramesdk: wire payload truncated in palette")
            for i in range(pal_n):
                nib = (raw[off + i // 2] & 0x0F) if (i % 2 == 0) else (raw[off + i // 2] >> 4)
                palette.append((pal_b[i], nib))
            if pal_n % 2 == 1 and (raw[off + f_bytes - 1] & 0xF0) != 0:
                raise ValueError("ramesdk: wire payload non-zero palette padding")
            off += f_bytes
        for i in range(1, pal_n):
            if palette[i] <= palette[i - 1]:
                raise ValueError("ramesdk: wire payload palette not sorted")
    elif dict_n > 0:
        for i in range(dict_n):
            dict_b[i], n = _read_varint(raw, off)
            off += n
        if wide_f:
            if off + dict_n > len(raw):
                raise ValueError("ramesdk: wire payload truncated in f-column")
            has_high = False
            for i in range(dict_n):
                dict_f[i] = raw[off + i]
                if raw[off + i] > 0x0F:
                    has_high = True
            if not has_high:
                raise ValueError("ramesdk: wire payload wide-f column without high nibble")
            off += dict_n
        else:
            f_bytes = (dict_n + 1) // 2
            if off + f_bytes > len(raw):
                raise ValueError("ramesdk: wire payload truncated in f-column")
            for i in range(dict_n):
                dict_f[i] = (raw[off + i // 2] & 0x0F) if (i % 2 == 0) else (raw[off + i // 2] >> 4)
            if dict_n % 2 == 1 and (raw[off + f_bytes - 1] & 0xF0) != 0:
                raise ValueError("ramesdk: wire payload non-zero f-column padding")
            off += f_bytes

    br = _BitReader(raw[off:])
    dict_h = [0] * dict_n
    prev_h = 0
    max_unary = 1 << (64 - gr_k + 1)
    for i in range(dict_n):
        if gr_k == 64:
            delta = br.read(64)
        else:
            q = 0
            while br.read(1) != 0:
                q += 1
                if q > max_unary:
                    raise ValueError("ramesdk: wire payload unary overflow")
            rem = br.read(gr_k)
            delta = (q << gr_k) | rem
        if i > 0 and prev_h + delta >= (1 << 64):
            raise ValueError("ramesdk: wire payload delta overflow")
        dict_h[i] = (prev_h + delta) & 0xFFFFFFFFFFFFFFFF
        prev_h = dict_h[i]

    if pal_n > 0:
        pal_bits = _bits_for_dict(pal_n)
        for i in range(dict_n):
            p_idx = br.read(pal_bits) if pal_bits > 0 else 0
            if p_idx >= pal_n:
                raise ValueError("ramesdk: wire payload palette index out of range")
            dict_b[i], dict_f[i] = palette[p_idx]

    dict_tuples = [(dict_h[i], dict_b[i], dict_f[i]) for i in range(dict_n)]
    for i in range(1, dict_n):
        if dict_tuples[i] <= dict_tuples[i - 1]:
            raise ValueError("ramesdk: wire payload dictionary not sorted")

    if identity:
        index = list(range(slot_n))
    elif dict_n == 1:
        index = [0] * slot_n
    elif dict_n > 1:
        index = [0] * slot_n
        remaining = list(range(dict_n))
        seen_list: List[int] = []
        for i in range(slot_n):
            if not seen_list:
                r_pos = br.read(_bits_for_dict(len(remaining)))
                if r_pos >= len(remaining):
                    raise ValueError("ramesdk: wire payload slot index out of range")
                d_idx = remaining.pop(r_pos)
                seen_list.append(d_idx)
                index[i] = d_idx
                continue
            is_repeat = br.read(1) if remaining else 1
            if is_repeat == 1:
                s_bits = _bits_for_dict(len(seen_list))
                sp = br.read(s_bits) if s_bits > 0 else 0
                if sp >= len(seen_list):
                    raise ValueError("ramesdk: wire payload seen index out of range")
                index[i] = seen_list[sp]
            else:
                rem_bits = _bits_for_dict(len(remaining))
                r_pos = br.read(rem_bits) if rem_bits > 0 else 0
                if r_pos >= len(remaining):
                    raise ValueError("ramesdk: wire payload remaining index out of range")
                d_idx = remaining.pop(r_pos)
                seen_list.append(d_idx)
                index[i] = d_idx
        if remaining:
            raise ValueError("ramesdk: wire payload unused dictionary entry")
    else:
        index = []

    br.verify_zero_padding()
    off += br.bytes_read()

    sent: List[int] = []
    prev_off = 0
    for _ in range(sent_n):
        delta_len, n = _read_varint32(raw, off)
        off += n
        next_off = prev_off + delta_len + 1
        if next_off <= prev_off or next_off > slot_n:
            raise ValueError("ramesdk: wire payload invalid sentence offset")
        sent.append(next_off)
        prev_off = next_off

    if off != len(raw):
        raise ValueError("ramesdk: wire payload trailing bytes")
    if sent and sent[-1] != slot_n:
        raise ValueError("ramesdk: last sentence offset != slotN")

    atoms = [{"h": dict_h[p], "b": max(10, dict_b[p]), "f": dict_f[p]} for p in index]
    frames: List[List[Dict[str, int]]] = []
    if sent:
        prev = 0
        for o in sent:
            frames.append(atoms[prev:o])
            prev = o
    return atoms, frames


def encode_wire_payload(
    slots: List[Dict[str, int]],
    frames: Optional[List[List[Dict[str, int]]]] = None,
) -> str:
    return base64.b64encode(encode_wire_bytes(slots, frames)).decode("ascii")


def decode_wire_payload(b64: str) -> Tuple[List[Dict[str, int]], List[List[Dict[str, int]]]]:
    return decode_wire_bytes(base64.b64decode(b64))


def derive_blind_pepper(secret: bytes) -> bytes:
    try:
        trimmed = trim_space(secret.decode("utf-8"))
    except UnicodeDecodeError:
        return secret
    if trimmed.startswith("mem2."):
        parts = trimmed.split(".")
        if len(parts) == 3:
            try:
                pad = "=" * (-len(parts[1]) % 4)
                claims = json.loads(base64.urlsafe_b64decode(parts[1] + pad).decode("utf-8"))
                tid = trim_space(str(claims.get("tid") or ""))
                uid = trim_space(str(claims.get("uid") or ""))
                if tid:
                    return f"rame-tenant-vault:{tid}:{uid}".encode("utf-8")
            except Exception:
                pass
    return secret


def extract_structured_date_harmonics(sent: str) -> List[str]:
    if not sent or len(sent) < 8:
        return []
    spans: List[Tuple[int, int, int, int]] = []
    n = len(sent)
    i = 0
    while i < n:
        if "0" <= sent[i] <= "9":
            start = i
            val = 0
            while i < n and "0" <= sent[i] <= "9":
                if i - start < 6:
                    val = val * 10 + (ord(sent[i]) - 48)
                i += 1
            spans.append((val, i - start, start, i))
        else:
            i += 1
    if len(spans) < 3:
        return []
    out: List[str] = []
    seen = set()
    idx = 0
    while idx + 2 < len(spans):
        y_val, y_dig, y_start, y_end = spans[idx]
        m_val, m_dig, m_start, m_end = spans[idx + 1]
        d_val, d_dig, d_start, d_end = spans[idx + 2]
        if (
            y_dig != 4
            or m_dig < 1
            or m_dig > 2
            or d_dig < 1
            or d_dig > 2
            or y_val < 1900
            or y_val > 2199
            or m_val < 1
            or m_val > 12
            or d_val < 1
            or d_val > 31
            or m_start - y_end != 1
            or d_start - m_end != 1
        ):
            idx += 1
            continue
        sep1, sep2 = sent[y_end], sent[m_end]
        if ("0" <= sep1 <= "9") or ("0" <= sep2 <= "9") or is_space(sep1) or is_space(sep2):
            idx += 1
            continue
        if idx > 0 and y_start - spans[idx - 1][3] == 1 and sent[spans[idx - 1][3]] in _WORD_CONNS:
            idx += 1
            continue
        if idx + 3 < len(spans) and spans[idx + 3][2] - d_end == 1 and sent[d_end] in _WORD_CONNS:
            idx += 1
            continue
        iso = f"{y_val:04d}-{m_val:02d}-{d_val:02d}"
        if iso not in seen:
            seen.add(iso)
            out.append(iso)
        idx += 3
    return out


class BlindEncoder:
    """Deterministic RAME-BlindPulse-v1 encoder."""

    def __init__(self, secret: Optional[bytes] = None, *, raw_key: Optional[bytes] = None) -> None:
        if raw_key is not None:
            if len(raw_key) != 32:
                raise ValueError("raw_key must be exactly 32 bytes")
            self.secret_key = bytes(raw_key)
        elif secret:
            self.secret_key = hashlib.sha256(derive_blind_pepper(secret)).digest()
        else:
            self.secret_key = os.urandom(32)
        self._enable_structured_dates = False
        self._harmonic_expander = None

    def set_canonical_harmonics(self, *, enable_structured_dates: bool = False, expander: Any = None) -> None:
        self._enable_structured_dates = bool(enable_structured_dates)
        self._harmonic_expander = expander

    def keyed_hash64(self, token: str) -> int:
        return struct.unpack("<Q", hmac.new(self.secret_key, token.encode("utf-8"), hashlib.sha256).digest()[:8])[0]

    def _pack_symbol_atoms(self, sent: str) -> List[Dict[str, int]]:
        atoms: List[Dict[str, int]] = []
        prev_r, run_len, has_lower = "", 0, False
        for idx, r in enumerate(sent):
            if is_space(r):
                continue
            lr = simple_lower_cp(r)
            run_len = run_len + 1 if lr == prev_r else 1
            prev_r = lr
            rw = carrier_units(r)
            if run_len >= 3:
                rw //= 4
            fl, has_lower = _classify_flag(r, idx > 0, idx > 0 and has_lower)
            atoms.append({"h": self.keyed_hash64(lr), "b": max(10, min(rw, 65535)), "f": fl})
        return atoms

    def _build_single_word_atom(self, w: str, word_idx: int, extra_flags: int = 0) -> Optional[Dict[str, int]]:
        low = simple_lower(trim_space(w))
        if not low:
            return None
        raw_width, flags, has_lower, prev_r, run_len = 0, extra_flags, False, "", 0
        for idx, r in enumerate(w):
            if is_space(r):
                continue
            lr = simple_lower_cp(r)
            run_len = run_len + 1 if lr == prev_r else 1
            prev_r = lr
            rw = carrier_units(r)
            if run_len >= 3:
                rw //= 4
            raw_width += rw
            fl, has_lower = _classify_flag(r, word_idx > 0 or idx > 0, idx > 0 and has_lower)
            flags |= fl
        return {"h": self.keyed_hash64(low), "b": max(10, min(raw_width, 65535)), "f": flags}

    @staticmethod
    def _adaptive_subcarrier_slices(tok: str) -> List[str]:
        rune_indices: List[int] = []
        rune_widths: List[int] = []
        total_w = 0
        for idx, r in enumerate(tok):
            if is_space(r):
                continue
            rune_indices.append(idx)
            rw = carrier_units(r)
            rune_widths.append(rw)
            total_w += rw
        rune_count = len(rune_indices)
        if rune_count < 4 or total_w <= 160:
            return []
        k = max(2, min(8, (total_w + 119) // 120, rune_count // 2))
        if k < 2:
            return []
        target_per_sub = max(40, total_w // k)
        out: List[str] = []
        start_pos, acc_w = 0, 0
        for i in range(rune_count):
            acc_w += rune_widths[i]
            rem_runes = rune_count - 1 - i
            rem_chunks = k - len(out) - 1
            if rem_chunks > 0 and acc_w >= target_per_sub and rem_runes >= rem_chunks * 2:
                out.append(tok[rune_indices[start_pos] : rune_indices[i + 1]])
                start_pos, acc_w = i + 1, 0
        if start_pos < rune_count:
            out.append(tok[rune_indices[start_pos] :])
        return out if len(out) >= 2 else []

    def _append_adaptive_harmonic_atoms(
        self, s_atoms: List[Dict[str, int]], cw_trim: str, word_idx: int, glued_threshold_x10: int
    ) -> List[Dict[str, int]]:
        parent_atom = self._build_single_word_atom(cw_trim, word_idx, 0)
        if not parent_atom:
            return s_atoms
        distinct_subs = [
            trim_space(sub) for sub in split_words(cw_trim) if trim_space(sub) and simple_lower(trim_space(sub)) != simple_lower(cw_trim)
        ]
        if len(distinct_subs) < 2 and parent_atom["b"] > glued_threshold_x10:
            distinct_subs = self._adaptive_subcarrier_slices(cw_trim)
        if len(distinct_subs) < 2:
            s_atoms.append(parent_atom)
            return s_atoms
        sub_atoms: List[Dict[str, int]] = []
        sub_strings: List[str] = []
        for sub_idx, sub in enumerate(distinct_subs):
            sa = self._build_single_word_atom(sub, word_idx + sub_idx, 0)
            if sa and sa["h"] != parent_atom["h"]:
                sub_atoms.append(sa)
                sub_strings.append(sub)
        if len(sub_atoms) < 2:
            s_atoms.append(parent_atom)
            return s_atoms
        if len(sub_atoms) <= 8:
            sum_sub_b = sum(sa["b"] for sa in sub_atoms)
            max_sub_b = max(sa["b"] for sa in sub_atoms)
            if max_sub_b >= 33 and sum_sub_b >= 60:
                parent_atom["f"] |= FLAG_HAS_CONN
                parent_atom["b"] = max(10, min(sum_sub_b, 65535))
                s_atoms.append(parent_atom)
                s_atoms.extend(sub_atoms)
                return s_atoms
            s_atoms.append(parent_atom)
            return s_atoms
        for start in range(0, len(sub_atoms), 8):
            chunk = sub_atoms[start : start + 8]
            if len(chunk) == 1:
                s_atoms.append(chunk[0])
                continue
            sum_chunk_b = sum(sa["b"] for sa in chunk)
            frame_parent = dict(parent_atom)
            if start > 0:
                fp = self._build_single_word_atom(" ".join(sub_strings[start : start + 8]), word_idx + start, FLAG_HAS_CONN)
                if fp:
                    frame_parent = fp
            frame_parent["f"] |= FLAG_HAS_CONN
            if sum_chunk_b > 65535:
                frame_parent["b"] = 65535
            elif sum_chunk_b >= 60:
                frame_parent["b"] = sum_chunk_b
            s_atoms.append(frame_parent)
            s_atoms.extend(chunk)
        return s_atoms

    @staticmethod
    def _carrier_width_clamped(w: str) -> int:
        raw_width, prev_r, run_len = 0, "", 0
        for r in w:
            lr = simple_lower_cp(r)
            run_len = run_len + 1 if lr == prev_r else 1
            prev_r = lr
            rw = carrier_units(r)
            if run_len >= 3:
                rw //= 4
            raw_width += rw
        return max(10, min(raw_width, 65535))

    def pack_sentence_atoms(self, sent: str) -> List[Dict[str, int]]:
        words = split_words(sent)
        if not words:
            return self._pack_symbol_atoms(sent)
        form_marks = blind_form_flag_marks(sent, words)
        atoms: List[Dict[str, int]] = []
        expanded_words = set() if self._harmonic_expander is not None else None
        for word_idx, w in enumerate(words):
            low = simple_lower(trim_space(w))
            if not low:
                continue
            raw_width, flags, has_lower, prev_r, run_len = 0, 0, False, "", 0
            for idx, r in enumerate(w):
                lr = simple_lower_cp(r)
                run_len = run_len + 1 if lr == prev_r else 1
                prev_r = lr
                rw = carrier_units(r)
                if run_len >= 3:
                    rw //= 4
                raw_width += rw
                fl, has_lower = _classify_flag(r, word_idx > 0 or idx > 0, idx > 0 and has_lower)
                flags |= fl
            clamped_w = max(10, min(raw_width, 65535))
            flags |= form_marks[word_idx]
            if clamped_w > 420 and len(split_compound_subwords(w)) < 2:
                prev_len = len(atoms)
                atoms = self._append_adaptive_harmonic_atoms(atoms, w, word_idx, 420)
                if len(atoms) > prev_len and form_marks[word_idx] != 0:
                    atoms[prev_len]["f"] |= form_marks[word_idx]
                continue
            if expanded_words is not None:
                expanded_words.add(low)
            self._append_word_with_canonical_harmonics(atoms, low, clamped_w, flags)
        if self._harmonic_expander is not None and expanded_words is not None:
            for sym_unit in extract_inline_symbol_units(sent):
                if sym_unit not in expanded_words:
                    expanded_words.add(sym_unit)
                    self._append_word_with_canonical_harmonics(
                        atoms, sym_unit, self._carrier_width_clamped(sym_unit), FLAG_IS_QUANTITY_OR_UNIT
                    )
            for sea_unit in extract_se_asian_unit_words(sent):
                if sea_unit not in expanded_words:
                    expanded_words.add(sea_unit)
                    self._append_word_with_canonical_harmonics(
                        atoms, sea_unit, self._carrier_width_clamped(sea_unit), 0
                    )
        if self._enable_structured_dates and atoms:
            existing = {a["h"] for a in atoms}
            for iso in extract_structured_date_harmonics(sent):
                cal_h = self.keyed_hash64("cal:" + iso)
                if cal_h not in existing:
                    existing.add(cal_h)
                    atoms.append(
                        {"h": cal_h, "b": CALENDAR_BEACON_WIDTH_X10, "f": FLAG_HAS_DIGIT | FLAG_HAS_CONN}
                    )
                h = self.keyed_hash64(iso)
                if h not in existing:
                    existing.add(h)
                    atoms.append({"h": h, "b": 284, "f": FLAG_HAS_DIGIT | FLAG_HAS_CONN})
        return atoms

    def _append_word_with_canonical_harmonics(
        self, atoms: List[Dict[str, int]], low: str, clamped_w: int, flags: int
    ) -> None:
        if self._harmonic_expander is None:
            atoms.append({"h": self.keyed_hash64(low), "b": clamped_w, "f": flags})
            return
        raw_tags = self._harmonic_expander(low) or []
        clean_tags: List[str] = []
        for t in raw_tags:
            tt = simple_lower(trim_space(str(t)))
            if tt and tt != low:
                clean_tags.append(tt)
                if len(clean_tags) == 2:
                    break
        if not clean_tags:
            atoms.append({"h": self.keyed_hash64(low), "b": clamped_w, "f": flags})
            return
        even_w = clamped_w
        if (even_w & 1) != 0:
            even_w = even_w + 1 if even_w < 65534 else even_w - 1
        if even_w < 24:
            even_w = 24
        primary_h = self.keyed_hash64(low)
        atoms.append({"h": primary_h, "b": even_w, "f": flags & ~FLAG_HAS_CONN})
        h1 = self.keyed_hash64(clean_tags[0])
        if h1 != primary_h:
            atoms.append({"h": h1, "b": even_w - 1, "f": FLAG_HAS_CONN | FLAG_IS_HARMONIC_COMPANION})
        if len(clean_tags) >= 2:
            h2 = self.keyed_hash64(clean_tags[1])
            if h2 != primary_h and h2 != h1:
                atoms.append({"h": h2, "b": even_w - 3, "f": FLAG_HAS_CONN | FLAG_IS_HARMONIC_COMPANION})

    def pack_blind_packet(self, doc_id: str, text: str, occurred_at: int = 0) -> Tuple[Dict[str, Any], List[str]]:
        trimmed = trim_space(text or "")
        if trimmed and not split_words(trimmed):
            sents = [trimmed]
        else:
            sents = split_sentences(text) or ([trimmed] if trimmed else [])
        all_slots: List[Dict[str, int]] = []
        sent_frames: List[List[Dict[str, int]]] = []
        valid_sents: List[str] = []
        for s in sents:
            s_atoms = self.pack_sentence_atoms(s)
            if s_atoms:
                valid_sents.append(s)
                sent_frames.append(s_atoms)
                all_slots.extend(s_atoms)
        pkt: Dict[str, Any] = {"id": doc_id, "slots": all_slots, "sents": sent_frames}
        if occurred_at:
            pkt["ts"] = occurred_at
        if all_slots:
            pkt["atoms_b64"] = encode_wire_payload(all_slots, sent_frames)
        return pkt, valid_sents

    def pack_blind_packet_with_meta(
        self,
        doc_id: str,
        text: str,
        author: str = "",
        occurred_at: int = 0,
        narrated_at: int = 0,
        ingested_at: int = 0,
        file_url: str = "",
        file_parse: str = "",
        block_id: str = "",
        doc_h: int = 0,
        block_h: int = 0,
        line_start: int = 0,
        line_end: int = 0,
    ) -> Tuple[Dict[str, Any], List[str]]:
        full_text = format_plaintext_with_meta(
            text, author=author, occurred_at=occurred_at, narrated_at=narrated_at, ingested_at=ingested_at, file_parse=file_parse
        )
        occ = occurred_at if occurred_at > 0 else (narrated_at if narrated_at > 0 else 0)
        pkt, valid_sents = self.pack_blind_packet(doc_id, full_text, occ)
        if narrated_at > 0:
            pkt["narrated_at"] = narrated_at
        if ingested_at > 0:
            pkt["ingested_at"] = ingested_at
        clean_author = simple_lower(trim_space(author or ""))
        if clean_author:
            pkt["author_h"] = self.keyed_hash64(clean_author)
        resolved_doc_h = doc_h if doc_h > 0 else (self.keyed_hash64(trim_space(file_url)) if trim_space(file_url or "") else 0)
        if resolved_doc_h > 0:
            pkt["doc_h"] = resolved_doc_h
        resolved_block_h = block_h if block_h > 0 else (self.keyed_hash64(trim_space(block_id)) if trim_space(block_id or "") else 0)
        if resolved_block_h > 0:
            pkt["block_h"] = resolved_block_h
        if line_start > 0:
            pkt["line_start"] = line_start
        if line_end > 0:
            pkt["line_end"] = max(line_start, line_end) if line_start > 0 else line_end
        elif line_start > 0:
            pkt["line_end"] = line_start
        return pkt, valid_sents

    def pack_composite_packets(
        self,
        parent_id: str,
        parent_text: str,
        occurred_at: int,
        sub_units: List[SubUnit],
    ) -> Tuple[List[Dict[str, Any]], Dict[str, str]]:
        """Encode one parent unit plus its high-resolution sub-units.

        Mirrors Go ``(*BlindEncoder).PackCompositePackets``: the parent and every
        sub-unit share ``doc_h = KeyedHash64(parentID)``; each sub-unit carries its
        own ``block_h`` (from ``block_id`` / sub id), line span, and cites the parent.
        """
        pid = trim_space(parent_id or "")
        parent_doc_h = self.keyed_hash64(pid) if pid else 0
        packets: List[Dict[str, Any]] = []
        sub_to_parent: Dict[str, str] = {}
        if pid and trim_space(parent_text or ""):
            parent_pkt, _ = self.pack_blind_packet(pid, parent_text, occurred_at)
            if parent_pkt.get("slots"):
                if parent_doc_h > 0:
                    parent_pkt["doc_h"] = parent_doc_h
                packets.append(parent_pkt)
        kind_counts: Dict[str, int] = {}
        for raw in sub_units or []:
            su = SubUnit.from_mapping(raw)
            txt = trim_space(su.text)
            fp = trim_space(su.file_parse)
            if not txt:
                txt = fp
            elif fp and fp not in txt:
                txt = txt + "\n" + fp
            if not txt:
                continue
            kind = trim_space(su.kind) or "sub"
            idx = kind_counts.get(kind, 0)
            kind_counts[kind] = idx + 1
            sub_id = trim_space(su.id) or compose_sub_unit_id(pid, kind, idx)
            sub_pkt, _ = self.pack_blind_packet(sub_id, txt, occurred_at)
            if not sub_pkt.get("slots"):
                continue
            if parent_doc_h > 0:
                sub_pkt["doc_h"] = parent_doc_h
            block_id = trim_space(su.block_id)
            if block_id:
                sub_pkt["block_h"] = self.keyed_hash64(block_id)
            elif sub_id:
                sub_pkt["block_h"] = self.keyed_hash64(sub_id)
            if su.line_start > 0:
                sub_pkt["line_start"] = su.line_start
            if su.line_end > 0:
                sub_pkt["line_end"] = su.line_end
            if su.line_start > 0 and sub_pkt.get("line_end", 0) < su.line_start:
                sub_pkt["line_end"] = su.line_start
            if pid:
                sub_pkt["cited_unit_ids"] = [pid]
                sub_to_parent[sub_id] = pid
                if idx == 0:
                    sub_to_parent[f"{pid}#{kind}:0"] = pid
            packets.append(sub_pkt)
        return packets, sub_to_parent


def verify_codec_local() -> Dict[str, Any]:
    enc = BlindEncoder(CANARY_SECRET.encode("utf-8"))
    pkt, sents = enc.pack_blind_packet(CANARY_DOC_ID, CANARY_TEXT, CANARY_TS)
    if len(sents) != CANARY_SENT_COUNT or len(pkt["slots"]) != CANARY_SLOT_COUNT:
        raise ValueError(f"ramesdk: local codec canary count mismatch (slots={len(pkt['slots'])} sents={len(sents)})")
    if pkt.get("atoms_b64") != CANARY_ATOMS_B64:
        raise ValueError(f"ramesdk: local codec canary atoms_b64 drift: {pkt.get('atoms_b64')} != {CANARY_ATOMS_B64}")
    dec_slots, dec_frames = decode_wire_payload(pkt["atoms_b64"])
    if dec_slots != pkt["slots"] or dec_frames != pkt["sents"]:
        raise ValueError("ramesdk: local codec canary round-trip mismatch")
    return {
        "local_verified": True,
        "spec_version": SPEC_VERSION,
        "slot_count": CANARY_SLOT_COUNT,
        "sent_count": CANARY_SENT_COUNT,
        "atoms_b64": CANARY_ATOMS_B64,
    }


def distill_plaintext(raw_text: str, all_sents: List[str], kept_idx: List[int]) -> Tuple[str, str, List[str]]:
    if not kept_idx or not all_sents or len(kept_idx) >= len(all_sents):
        return raw_text, raw_text, all_sents
    kept_sents = [all_sents[i] for i in kept_idx if 0 <= i < len(all_sents)]
    if not kept_sents or len(kept_sents) >= len(all_sents):
        return raw_text, raw_text, all_sents
    sep = "\n" if "\n" in raw_text else ". "
    return raw_text, sep.join(kept_sents), kept_sents


class PlaintextStore(Protocol):
    """Bring-Your-Own-Storage (BYO-Storage) protocol for user plaintext."""

    def pack_blind_packet(self, doc_id: str, text: str, occurred_at: int = 0) -> Tuple[Dict[str, Any], List[str]]: ...
    def save_local_plaintext(self, doc_id: str, occurred_at: int, raw_text: str, sents: List[str], resp: Dict[str, Any]) -> None: ...
    def load_local_document(self, doc_id: str) -> Tuple[str, str, List[str]]: ...


class MemoryStore:
    """In-memory / dict-backed PlaintextStore implementation."""

    def __init__(self, secret: bytes) -> None:
        self.encoder = BlindEncoder(secret)
        self.docs: Dict[str, Dict[str, Any]] = {}

    def pack_blind_packet(self, doc_id: str, text: str, occurred_at: int = 0) -> Tuple[Dict[str, Any], List[str]]:
        return self.encoder.pack_blind_packet(doc_id, text, occurred_at)

    def save_local_plaintext(self, doc_id: str, occurred_at: int, raw_text: str, sents: List[str], resp: Dict[str, Any]) -> None:
        self.docs[doc_id] = {
            "raw_text": raw_text,
            "sentences": list(sents),
            "kept_indices": list(resp.get("kept_sentence_indices") or []),
            "occurred_at": occurred_at,
            "address_uri": resp.get("address_uri", ""),
        }

    def load_local_document(self, doc_id: str) -> Tuple[str, str, List[str]]:
        rec = self.docs[doc_id]
        return distill_plaintext(rec["raw_text"], rec["sentences"], rec["kept_indices"])


def _to_wire_packet(pkt: Dict[str, Any]) -> Dict[str, Any]:
    out = dict(pkt)
    if not out.get("atoms_b64"):
        slots = out.get("slots") or []
        sents = out.get("sents") or []
        out["atoms_b64"] = encode_wire_payload(slots, sents)
    out.pop("slots", None)
    out.pop("sents", None)
    return out


class RameClient:
    """HTTP Client for `/open/memory/v1/*`."""

    def __init__(
        self,
        endpoint: str,
        api_token: str,
        user_id: str,
        store: PlaintextStore,
        scope: str = "",
        tenant_id: str = "",
        assistant_id: Optional[str] = None,
        incarnation_id: Optional[str] = None,
        mode: Optional[str] = None,
        plaintext_mode: bool = False,
    ) -> None:
        self.endpoint = trim_space(endpoint).rstrip("/")
        self.api_token = trim_space(api_token)
        self.user_id = trim_space(user_id)
        self.tenant_id = trim_space(tenant_id) or self.user_id
        self.scope = trim_space(scope)
        self.assistant_id = trim_space(assistant_id if assistant_id is not None else (os.environ.get("ROLLING_MEMORY_ASSISTANT_ID") or "default"))
        self.incarnation_id = trim_space(incarnation_id if incarnation_id is not None else (os.environ.get("ROLLING_MEMORY_INCARNATION_ID") or ""))
        env_mode = os.environ.get("ROLLING_MEMORY_PULSE_MODE") or os.environ.get("ROLLING_MEMORY_MODE") or ""
        resolved_mode = trim_space(mode if mode is not None else env_mode).lower()
        if resolved_mode not in (MODE_LOCAL_PLAIN, MODE_PULSE_PLAIN, MODE_PULSE_CIPHER):
            resolved_mode = MODE_LOCAL_PLAIN if not self.endpoint else MODE_PULSE_CIPHER
        self.mode = resolved_mode
        self.plaintext_mode = bool(plaintext_mode) or resolved_mode == MODE_PULSE_PLAIN
        self.store = store

    def set_pulse_mode(self, mode: str) -> None:
        m = trim_space(mode).lower()
        if m in ("", MODE_PULSE_CIPHER):
            self.mode = MODE_PULSE_CIPHER
            self.plaintext_mode = False
        elif m == MODE_PULSE_PLAIN:
            self.mode = MODE_PULSE_PLAIN
            self.plaintext_mode = True
        elif m == MODE_LOCAL_PLAIN:
            self.mode = MODE_LOCAL_PLAIN
            self.plaintext_mode = False
        else:
            raise ValueError(f"ramesdk: invalid pulse_mode {mode!r}")

    def effective_cloud_pulse_mode(self) -> str:
        return MODE_PULSE_PLAIN if self.is_plaintext_mode() else MODE_PULSE_CIPHER

    def is_plaintext_mode(self) -> bool:
        return bool(self.plaintext_mode) or trim_space(self.mode).lower() == MODE_PULSE_PLAIN

    def _headers(self, has_body: bool = True) -> Dict[str, str]:
        headers: Dict[str, str] = {}
        if has_body:
            headers["Content-Type"] = "application/json"
        if self.api_token:
            headers["Authorization"] = f"Bearer {self.api_token}"
            headers["X-Api-Key"] = self.api_token
        if self.user_id:
            headers["X-Rolling-Memory-User"] = self.user_id
        if self.tenant_id:
            headers["X-Rolling-Memory-Tenant"] = self.tenant_id
        if self.scope:
            headers["X-Rolling-Memory-Scope"] = self.scope
        if self.assistant_id:
            headers["X-Rolling-Memory-Assistant"] = self.assistant_id
        if self.incarnation_id:
            headers["X-Rolling-Memory-Incarnation"] = self.incarnation_id
        return headers

    def _post_json(self, sub_path: str, payload: Dict[str, Any]) -> Any:
        req = urllib.request.Request(
            self.endpoint + sub_path,
            data=json.dumps(payload).encode("utf-8"),
            headers=self._headers(True),
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode("utf-8"))

    def _get_json(self, sub_path: str) -> Any:
        req = urllib.request.Request(self.endpoint + sub_path, headers=self._headers(False), method="GET")
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode("utf-8"))

    def health(self) -> Dict[str, Any]:
        verify_codec_local()
        resp = self._get_json("/health")
        if resp.get("status") != "ok" or resp.get("engine") != "autonoetic" or resp.get("version") != "v1":
            raise ValueError(f"ramesdk: unhealthy response: {resp}")
        canary = resp.get("codec_canary")
        if isinstance(canary, dict) and canary.get("atoms_b64"):
            if canary.get("atoms_b64") != CANARY_ATOMS_B64 or canary.get("slot_count") != CANARY_SLOT_COUNT:
                raise ValueError(f"ramesdk: remote codec canary mismatch: {canary}")
        return resp

    def verify_codec(self) -> Dict[str, Any]:
        report = verify_codec_local()
        if not self.endpoint:
            return report
        try:
            resp = self._post_json("/health", {"atoms_b64": CANARY_ATOMS_B64})
        except Exception:
            resp = self._get_json("/health?atoms_b64=" + urllib.parse.quote(CANARY_ATOMS_B64, safe=""))
        if resp.get("status") != "ok" or resp.get("engine") != "autonoetic" or resp.get("version") != "v1":
            raise ValueError(f"ramesdk: unhealthy response: {resp}")
        canary = resp.get("codec_canary")
        if isinstance(canary, dict) and canary.get("atoms_b64"):
            if canary.get("atoms_b64") != CANARY_ATOMS_B64 or canary.get("slot_count") != CANARY_SLOT_COUNT:
                raise ValueError(f"ramesdk: remote codec canary mismatch: {canary}")
            report["remote_canary_ok"] = True
        probe = resp.get("codec_probe")
        if isinstance(probe, dict):
            if not probe.get("verified") or probe.get("atoms_b64") != CANARY_ATOMS_B64 or probe.get("slot_count") != CANARY_SLOT_COUNT:
                raise ValueError(f"ramesdk: remote codec probe verification failed: {probe}")
            report["remote_probe_ok"] = True
        return report

    def remember(self, doc_id: str, raw_text: str, occurred_at: int = 0, layer: str = "episodic") -> Dict[str, Any]:
        pkt, sents = self.store.pack_blind_packet(doc_id, raw_text, occurred_at)
        wire_pkt = _to_wire_packet(pkt)
        pulse_mode = self.effective_cloud_pulse_mode()
        wire_pkt["pulse_mode"] = pulse_mode
        if self.is_plaintext_mode() and trim_space(raw_text):
            wire_pkt["raw_text"] = raw_text
        wire_pkt["tenant_id"] = self.tenant_id
        wire_pkt["user_id"] = self.user_id
        wire_pkt["layer"] = trim_space(layer) or "episodic"
        if self.scope:
            wire_pkt["scope"] = self.scope
        if self.assistant_id:
            wire_pkt["assistant_id"] = self.assistant_id
        if self.incarnation_id:
            wire_pkt["incarnation_id"] = self.incarnation_id
        resp: Dict[str, Any] = {"unit_id": doc_id, "layer": wire_pkt["layer"], "pulse_mode": pulse_mode}
        if self.endpoint and trim_space(self.mode).lower() != MODE_LOCAL_PLAIN:
            resp = self._post_json("/ingest", wire_pkt)
        self.store.save_local_plaintext(doc_id, occurred_at, raw_text, sents, resp)
        return resp

    def recall(self, query_text: str, top_k: int = 0) -> List[Dict[str, Any]]:
        q_pkt, _ = self.store.pack_blind_packet("QUERY", query_text, 0)
        k = top_k if top_k > 0 else 0
        pulse_mode = self.effective_cloud_pulse_mode()
        wire_query = _to_wire_packet(q_pkt)
        wire_query["pulse_mode"] = pulse_mode
        if self.is_plaintext_mode():
            wire_query["raw_text"] = query_text
        payload: Dict[str, Any] = {
            "tenant_id": self.tenant_id,
            "user_id": self.user_id,
            "pulse_mode": pulse_mode,
            "query": wire_query,
            "top_k": k,
        }
        if self.is_plaintext_mode():
            payload["query_text"] = query_text
        if k == 0:
            payload["delivery_budget"] = "auto"
        if self.scope:
            payload["scope"] = self.scope
        if self.assistant_id:
            payload["assistant_id"] = self.assistant_id
        if self.incarnation_id:
            payload["incarnation_id"] = self.incarnation_id
        raw = self._post_json("/query?envelope=1", payload)
        hits = raw.get("hits", []) if isinstance(raw, dict) else raw
        resolved: List[Dict[str, Any]] = []
        for h in hits:
            uid = h.get("unit_id") or h.get("id", "")
            raw_t, dist_t, kept_s = "", "", []
            if self.is_plaintext_mode() and trim_space(str(h.get("raw_text") or "")):
                raw_t = str(h.get("raw_text") or "")
                dist_t = str(h.get("distilled_text") or "") or raw_t
                kept_s = split_sentences(dist_t)
            else:
                try:
                    raw_t, dist_t, kept_s = self.store.load_local_document(uid)
                except Exception:
                    if trim_space(str(h.get("raw_text") or "")):
                        raw_t = str(h.get("raw_text") or "")
                        dist_t = str(h.get("distilled_text") or "") or raw_t
                        kept_s = split_sentences(dist_t)
                    else:
                        continue
            resolved.append({**h, "raw_plaintext": raw_t, "distilled_plaintext": dist_t, "kept_sentences": kept_s})
        return resolved



_ISO3166_GEO_ENTRIES: Tuple[str, ...] = (
    "@geo:cont:eu|@dim:geo:europe,european,europeans,europa,européen,européenne,europeo,europea,欧洲,歐洲,ヨーロッパ,欧州,유럽,구주,европа,ยุโรป,scandinavia,scandinavian,nordic,北欧,北歐,스칸디나비아,balkans,balkan,巴尔干,mediterranean,地中海",
    "@geo:cont:as|@dim:geo:asia,asian,asians,asien,asie,asiatico,asiatique,亚洲,亞洲,アジア,亜州,아시아,азия,เอเชีย,eastasia,东亚,東亞,southeastasia,东南亚,東南亞,동남아시아,東南アジア,southasia,南亚,centralasia,中亚",
    "@geo:cont:me|@geo:cont:as:middleeast,middle-east,mideast,nahost,moyen-orient,中东,中東,중동,ตะวันออกกลาง",
    "@geo:cont:am|@dim:geo:america,americas,american,americans,northamerica,north-america,northamerican,southamerica,south-america,latinamerica,centralamerica,amerika,amérique,美洲,北美,北美洲,南美,南美洲,拉美,拉丁美洲,アメリカ州,北米,南米,북미,남미,미주,아메리카,ทวีปอเมริกา",
    "@geo:cont:af|@dim:geo:africa,african,africans,afrika,afrique,africano,非洲,アフリカ,아프리카,африка,แอฟริกา,northafrica,北非,sub-saharan",
    "@geo:cont:oc|@dim:geo:oceania,oceanian,australasia,ozeanien,océanie,大洋洲,オセアニア,오세아니아,โอเชียเนีย",
    "@geo:cc:gb|@geo:cont:eu:uk,u.k.,gb,gbr,britain,greatbritain,unitedkingdom,british,england,english,scotland,scottish,wales,welsh,northernireland,英国,英國,英格兰,英格蘭,苏格兰,蘇格蘭,威尔士,威爾士,北爱尔兰,イギリス,英国人,イングランド,スコットランド,ウェールズ,영국,잉글랜드,스코틀랜드,웨일스,großbritannien,vereinigtenkönigreich,royaume-uni,angleterre,écosse,reinounido,inglaterra,escocia,regnounito,สหราชอาณาจักร,อังกฤษ,london,greaterlondon,westminster,soho,hillingdon,heathrow,gatwick,stansted,cambridge,cambridgeshire,peterborough,petersfield,oxford,oxfordshire,manchester,birmingham,liverpool,leeds,sheffield,bristol,newcastle,nottingham,southampton,portsmouth,brighton,leicester,coventry,york,yorkshire,warwick,somerset,canterbury,kent,windsor,surrey,sussex,essex,norfolk,norwich,suffolk,devon,exeter,cornwall,edinburgh,glasgow,aberdeen,dundee,inverness,stirling,standrews,highlands,cardiff,swansea,newport,belfast,derry,伦敦,倫敦,剑桥,劍橋,牛津,曼彻斯特,伯明翰,利物浦,爱丁堡,愛丁堡,格拉斯哥,卡迪夫,贝尔法斯特,巴斯,约克,ロンドン,ケンブリッジ,オックスフォード,マンチェスター,エジンバラ,グラスゴー,런던,케임브리지,옥스퍼드,맨체스터,에든버러,글래스고,marazion,penzance,ives,scilly,glastonbury,crawley,willowbrae,tyne,northumberland,cumbria,lancashire,derbyshire,nottinghamshire,leicestershire,warwickshire,gloucestershire,dorset,hampshire,康沃尔,苏塞克斯,约克郡,牛津郡,诺福克,威斯敏斯特,コーンウォール,サセックス,ヨークシャー,ノーフォーク,ウェストミンスター,콘월,서식스,요크셔,노퍽,웨스트민스터,cornouailles,คอร์นวอลล์",
    "@geo:cc:fr|@geo:cont:eu:france,french,metropolitanfrance,frankreich,francia,frança,法国,法國,法兰西,フランス,프랑스,франция,ฝรั่งเศส,ile-de-france,île-de-france,provence,provence-alpes-côte,côted'azur,azur,vaucluse,bouches-du-rhône,rhône,var,alpes-maritimes,normandy,normandie,brittany,bretagne,alsace,lorraine,burgundy,bourgogne,aquitaine,occitanie,auvergne,loire,corsica,corse,savoie,chamonix,paris,marseille,lyon,toulouse,nantes,strasbourg,montpellier,bordeaux,lille,rennes,reims,toulon,saint-étienne,leharve,grenoble,dijon,angers,nîmes,villeurbanne,aix-en-provence,cannes,antibes,avignon,cassis,marignane,istres,puyvert,pertuis,la-seyne-sur-mer,seyne-sur-mer,sablettes,saint-elme,pourquier,annecy,versailles,rouen,caen,nancy,metz,orléans,clermont-ferrand,limoges,perpignan,besançon,brest,biarritz,巴黎,马赛,馬賽,里昂,图卢兹,尼斯,南特,斯特拉斯堡,波尔多,里尔,土伦,戛纳,阿维尼翁,普罗旺斯,凡尔赛,パリ,マルセイユ,リヨン,ニース,ボルドー,カンヌ,プロヴァンス,파리,마르세유,리옹,니스,보르도,칸,프로방스,rhone,alpes,aix,montmartre,dunkerque,蔚蓝海岸,阿尔卑斯,霞慕尼,诺曼底,布列塔尼,蒙马特,コートダジュール,アルプス,シャモニー,ノルマンディー,ブルターニュ,ストラスブール,トゥールーズ,アヴィニョン,ヴェルサイユ,モンマルトル,코트다쥐르,알프스,샤모니,노르망디,브르타뉴,스트라스부르,툴루즈,아비뇽,베르사유,몽마르트르,provenza,normandía,bretaña,versalles,โพรวองซ์,แอลป์,นอร์มังดี,แวร์ซาย",
    "@geo:cc:de|@geo:cont:eu:germany,german,deutschland,deutsch,allemagne,alemania,alemanha,germania,德国,德國,德意志,ドイツ,독일,германия,เยอรมนี,bavaria,bayern,baden-württemberg,hessen,nrw,nordrhein-westfalen,sachsen,saxony,berlin,munich,münchen,hamburg,frankfurt,cologne,köln,stuttgart,düsseldorf,dortmund,essen,leipzig,bremen,dresden,hanover,hannover,nuremberg,nürnberg,duisburg,bochum,wuppertal,bielefeld,bonn,münster,karlsruhe,mannheim,augsburg,wiesbaden,heidelberg,freiburg,aachen,kiel,lübeck,rostock,potsdam,erfurt,mainz,saarbrücken,regensburg,würzburg,ulm,göttingen,tübingen,darmstadt,weimar,柏林,慕尼黑,汉堡,漢堡,法兰克福,法蘭克福,科隆,斯图加特,杜塞尔多夫,莱比锡,德累斯顿,汉诺威,纽伦堡,波恩,海德堡,巴伐利亚,ベルリン,ミュンヘン,ハンブルク,フランクフルト,ケルン,シュトゥットガルト,ハイデルベルク,베를린,뮌헨,함부르크,프랑크푸르트,쾰른,슈투트가르트,하이델베르크",
    "@geo:cc:it|@geo:cont:eu:italy,italian,italia,italien,italie,意大利,義大利,イタリア,이탈리아,италия,อิตาลี,lombardy,lombardia,tuscany,toscana,veneto,lazio,piedmont,piemonte,sicily,sicilia,sardinia,sardegna,emilia-romagna,campania,liguria,puglia,rome,roma,milan,milano,portello,naples,napoli,turin,torino,palermo,genoa,genova,bologna,florence,firenze,bari,catania,venice,venezia,verona,messina,padua,padova,trieste,brescia,parma,modena,pisa,siena,perugia,ravenna,rimini,salerno,bergamo,como,amalfi,capri,pompeii,罗马,羅馬,米兰,米蘭,那不勒斯,都灵,佛罗伦萨,佛羅倫斯,威尼斯,博洛尼亚,热那亚,维罗纳,比萨,西西里,托斯卡纳,ローマ,ミラノ,ナポリ,トリノ,フィレンツェ,ヴェネツィア,ベネチア,ボローニャ,ピサ,로마,밀라노,나폴리,토리노,피렌체,베네치아,베니스,볼로냐,피사,positano,伦巴第,撒丁岛,科莫,贝尔加莫,帕多瓦,锡耶纳,阿马尔菲,卡普里,庞贝,巴勒莫,ロンバルディア,トスカーナ,シチリア,サルデーニャ,コモ,ベルガモ,ヴェローナ,シエナ,ジェノヴァ,アマルフィ,カプリ,ポンペイ,パレルモ,롬바르디아,토스카나,시칠리아,사르데냐,코모,베르가모,베로나,시에나,제노바,아말피,카프리,폼페이,팔레르모,lombardei,toskana,sizilien,sardinien,lombardie,toscane,sicile,sardaigne,ลอมบาร์เดีย,ทัสคานี,ซิซิลี",
    "@geo:cc:es|@geo:cont:eu:spain,spanish,españa,spanien,espagne,spagna,espanha,西班牙,スペイン,스페인,испания,สเปน,catalonia,cataluña,catalunya,andalusia,andalucía,galicia,basque,euskadi,valencia,castile,aragon,balearic,canary,madrid,barcelona,seville,sevilla,zaragoza,málaga,malaga,murcia,palma,mallorca,ibiza,tenerife,laspalmas,bilbao,alicante,córdoba,cordoba,valladolid,vigo,gijón,granada,vitoria,coruña,acoruña,santiagodecompostela,compostela,toledo,salamanca,san-sebastián,donostia,pamplona,santander,tarragona,girona,marbella,马德里,馬德里,巴塞罗那,巴塞隆納,塞维利亚,瓦伦西亚,毕尔巴鄂,格拉纳达,马拉加,圣地亚哥-德孔波斯特拉,加利西亚,加泰罗尼亚,安达卢西亚,マドリード,バルセロナ,セビリア,バレンシア,ビルバオ,グラナダ,サンティアゴ・デ・コンポステーラ,마드리드,바르셀로나,세비야,발렌시아,빌바오,그라나다,산티아고데콤포스텔라,santiago,coruna,ensanche,圣地亚哥,孔波斯特拉,萨拉戈萨,科尔多瓦,托莱多,马略卡,サンティアゴ,コンポステーラ,ガリシア,カタルーニャ,アンダルシア,コルドバ,トレド,マヨルカ,산티아고,콤포스텔라,갈리시아,카탈루냐,안달루시아,코르도바,톨레도,마요르카,galicien,katalonien,andalusien,galice,catalogne,andalousie,ซานติอาโก,กาลิเซีย,กาตาลุญญา",
    "@geo:cc:pt|@geo:cont:eu:portugal,portuguese,portogallo,葡萄牙,ポルトガル,포르투갈,португалия,โปรตุเกส,lisbon,lisboa,porto,oporto,gaia,vilaisabel,vilanovadegaia,ribeira,cedofeita,braga,coimbra,faro,algarve,aveiro,funchal,madeira,azores,sintra,cascais,évora,guimarães,里斯本,波尔图,波爾圖,科英布拉,法鲁,阿爾加維,马德拉,辛特拉,リスボン,ポルト,コインブラ,리스본,포르투,코임브라,miragaia,marinha,加亚新城,布拉加,卡斯凯什,阿尔加维,亚速尔,ガイア,ブラガ,シントラ,アルガルヴェ,マデイラ,가이아,브라가,신트라,알가르브,마데이라,ปอร์โต,ลิสบอน,มาเดรา",
    "@geo:cc:nl|@geo:cont:eu:netherlands,dutch,holland,nederland,niederlande,pays-bas,paísesbajos,荷兰,荷蘭,オランダ,네덜란드,เนเธอร์แลนด์,amsterdam,rotterdam,thehague,denhaag,hague,utrecht,eindhoven,groningen,tilburg,almere,breda,nijmegen,delft,leiden,haarlem,maastricht,arnhem,schiphol,阿姆斯特丹,鹿特丹,海牙,乌得勒支,埃因霍温,代尔夫特,莱顿,马斯特里赫特,アムステルダム,ロッテルダム,ハーグ,ユトレヒト,암스테르담,로테르담,헤이그,위트레흐트",
    "@geo:cc:be|@geo:cont:eu:belgium,belgian,belgië,belgique,belgien,bélgica,比利时,比利時,ベルギー,벨기에,เบลเยียม,brussels,bruxelles,brussel,antwerp,antwerpen,ghent,gent,charleroi,liège,liege,bruges,brugge,leuven,namur,布鲁塞尔,布魯塞爾,安特卫普,根特,布鲁日,鲁汶,ブリュッセル,アントワープ,ブルージュ,브뤼셀,앤트워프,브뤼헤",
    "@geo:cc:lu|@geo:cont:eu:luxembourg,luxemburg,卢森堡,盧森堡,ルクセンブルク,룩셈부르크",
    "@geo:cc:ch|@geo:cont:eu:switzerland,swiss,schweiz,suisse,svizzera,suiza,helvetica,瑞士,スイス,스위스,สวิตเซอร์แลนด์,zurich,zürich,geneva,genève,genf,basel,lausanne,bern,berne,winterthur,lucerne,luzern,st.gallen,lugano,interlaken,zermatt,davos,montreux,zug,苏黎世,蘇黎世,日内瓦,日內瓦,巴塞尔,洛桑,伯尔尼,卢塞恩,琉森,因特拉肯,采尔马特,达沃斯,チューリッヒ,ジュネーブ,バーゼル,ベルン,ルツェルン,취리히,제네바,바젤,베른,루체른",
    "@geo:cc:at|@geo:cont:eu:austria,austrian,österreich,autriche,奥地利,奧地利,オーストリア,오스트리아,ออสเตรีย,vienna,wien,graz,linz,salzburg,innsbruck,klagenfurt,villach,hallstatt,tyrol,tirol,维也纳,維也納,萨尔茨堡,薩爾茨堡,因斯布鲁克,格拉茨,林茨,哈尔施塔特,ウィーン,ザルツブルク,インスブルック,빈,비엔나,잘츠부르크,인스브루크",
    "@geo:cc:ie|@geo:cont:eu:ireland,irish,éire,irland,irlande,irlanda,爱尔兰,愛爾蘭,アイルランド,아일랜드,ไอร์แลนด์,dublin,cork,limerick,galway,waterford,kilkenny,都柏林,科克,高威,ダブリン,더블린",
    "@geo:cc:se|@geo:cont:eu:sweden,swedish,sverige,schweden,suède,suecia,瑞典,スウェーデン,스웨덴,สวีเดน,stockholm,gothenburg,göteborg,malmö,malmo,uppsala,lund,linköping,kiruna,斯德哥尔摩,哥德堡,马尔默,乌普萨拉,ストックホルム,스톡홀름",
    "@geo:cc:no|@geo:cont:eu:norway,norwegian,norge,norwegen,norvège,noruega,挪威,ノルウェー,노르웨이,นอร์เวย์,oslo,bergen,trondheim,stavanger,tromsø,tromso,ålesund,lofoten,奥斯陆,奧斯陸,卑尔根,特罗姆瑟,オスロ,오슬로",
    "@geo:cc:dk|@geo:cont:eu:denmark,danish,danmark,dänemark,danemark,dinamarca,丹麦,丹麥,デンマーク,덴마크,เดนมาร์ก,copenhagen,københavn,aarhus,odense,aalborg,roskilde,billund,哥本哈根,奥胡斯,欧登塞,コペンハーゲン,코펜하겐",
    "@geo:cc:fi|@geo:cont:eu:finland,finnish,suomi,finnland,finlande,finlandia,芬兰,芬蘭,フィンランド,핀란드,ฟินแลนด์,helsinki,espoo,tampere,vantaa,oulu,turku,rovaniemi,lapland,赫尔辛基,赫爾辛基,坦佩雷,图尔库,罗瓦涅米,ヘルシンキ,헬싱키",
    "@geo:cc:is|@geo:cont:eu:iceland,icelandic,ísland,island,islande,islandia,冰岛,冰島,アイスランド,아이슬란드,ไอซ์แลนด์,reykjavik,reykjavík,akureyri,keflavik,雷克雅未克,レイキャビク,레이캬비크",
    "@geo:cc:pl|@geo:cont:eu:poland,polish,polska,polen,pologne,polonia,波兰,波蘭,ポーランド,폴란드,โปแลนด์,warsaw,warszawa,kraków,krakow,cracow,łódź,wrocław,wroclaw,poznań,poznan,gdańsk,gdansk,katowice,lublin,华沙,華沙,克拉科夫,格但斯克,弗罗茨瓦夫,波兹南,ワルシャワ,クラクフ,바르샤바,크라쿠프",
    "@geo:cc:cz|@geo:cont:eu:czechia,czech,czechrepublic,česko,tschechien,tchèque,捷克,チェコ,체코,เช็ก,prague,praha,brno,ostrava,plzeň,karlovyvary,českýkrumlov,bohemia,moravia,布拉格,布尔诺,プラハ,프라하",
    "@geo:cc:hu|@geo:cont:eu:hungary,hungarian,magyarország,ungarn,hongrie,hungría,ungheria,匈牙利,ハンガリー,헝가리,ฮังการี,budapest,ferihegy,debrecen,szeged,miskolc,pécs,győr,balaton,布达佩斯,布達佩斯,ブダペスト,부다페스트",
    "@geo:cc:sk|@geo:cont:eu:slovakia,slovak,slovensko,slowakei,slovaquie,斯洛伐克,スロバキア,슬로바키아,bratislava,košice,布拉迪斯拉发,ブラチスラバ,브라티슬라바",
    "@geo:cc:si|@geo:cont:eu:slovenia,slovenian,slovenija,slowenien,slovénie,斯洛文尼亚,スロベニア,슬로베니아,ljubljana,maribor,bled,卢布尔雅那,リュブリャナ,류블랴나",
    "@geo:cc:hr|@geo:cont:eu:croatia,croatian,hrvatska,kroatien,croatie,croazia,克罗地亚,克羅埃西亞,クロアチア,크로아티아,zagreb,rijeka,dubrovnik,zadar,pula,istria,dalmatia,萨格勒布,杜布罗夫尼克,斯普利特,ザグレブ,ドブロブニク,자그레브,두브로브니크",
    "@geo:cc:rs|@geo:cont:eu:serbia,serbian,srbija,serbien,serbie,塞尔维亚,塞爾維亞,セルビア,세르비아,belgrade,beograd,novisad,niš,贝尔格莱德,ベオグラード,베오그라드",
    "@geo:cc:ro|@geo:cont:eu:romania,romanian,românia,rumänien,roumanie,罗马尼亚,羅馬尼亞,ルーマニア,루마니아,bucharest,bucurești,cluj,cluj-napoca,timișoara,iași,constanța,brașov,transylvania,布加勒斯特,克卢日,ブカレスト,부쿠레슈티",
    "@geo:cc:bg|@geo:cont:eu:bulgaria,bulgarian,bulgarien,bulgarie,保加利亚,保加利亞,ブルガリア,불가리아,sofia,plovdiv,varna,burgas,索非亚,ソフィア,소피아",
    "@geo:cc:gr|@geo:cont:eu:greece,greek,hellas,griechenland,grèce,grecia,希腊,希臘,ギリシャ,그리스,กรีซ,athens,athina,thessaloniki,patras,heraklion,crete,santorini,mykonos,rhodes,corfu,delphi,雅典,塞萨洛尼基,克里特,圣托里尼,米科诺斯,アテネ,サントリーニ,아테네,산토리니",
    "@geo:cc:ua|@geo:cont:eu:ukraine,ukrainian,ukraina,乌克兰,烏克蘭,ウクライナ,우크라이나,kyiv,kiev,kharkiv,odesa,odessa,dnipro,lviv,基辅,利沃夫,敖德萨,キーウ,키이우",
    "@geo:cc:ee|@geo:cont:eu:estonia,estonian,eesti,estland,爱沙尼亚,エストニア,에스토니아,tallinn,tartu,塔林,タリン,탈린",
    "@geo:cc:lv|@geo:cont:eu:latvia,latvian,latvija,lettland,拉脱维亚,ラトビア,라트비아,riga,里加,リガ,리가",
    "@geo:cc:lt|@geo:cont:eu:lithuania,lithuanian,lietuva,litauen,立陶宛,リトアニア,리투아니아,vilnius,kaunas,维尔纽斯,ビリニュス,빌뉴스",
    "@geo:cc:mt|@geo:cont:eu:malta,maltese,马耳他,マルタ,몰타,valletta,瓦莱塔",
    "@geo:cc:cy|@geo:cont:eu:cyprus,cypriot,塞浦路斯,キプロス,키프로스,nicosia,limassol,larnaca,尼科西亚",
    "@geo:cc:mc|@geo:cont:eu:monaco,摩纳哥,モナコ,모나코,montecarlo,蒙特卡洛",
    "@geo:cc:tr|@geo:cont:eu:turkey,turkish,türkiye,türkei,turquie,turquía,土耳其,トルコ,터키,튀르키예,ตุรกี,istanbul,ankara,izmir,antalya,bursa,adana,gaziantep,konya,cappadocia,bodrum,伊斯坦布尔,伊斯坦堡,安卡拉,安塔利亚,卡帕多奇亚,イスタンブール,アンカラ,이스탄불,앙카라",
    "@geo:cc:ru|@geo:cont:eu:russia,russian,россия,russland,russie,rusia,俄罗斯,俄羅斯,俄国,ロシア,러시아,รัสเซีย,moscow,moskva,st.petersburg,saintpetersburg,petersburg,novosibirsk,yekaterinburg,kazan,vladivostok,sochi,siberia,莫斯科,圣彼得堡,海参崴,西伯利亚,モスクワ,サンクトペテルブルク,모스크바,상트페테르부르크",
    "@geo:cc:cn|@geo:cont:as:china,chinese,prc,chine,cina,中国,中國,中华,中華,中国大陆,內地,中国語,チャイナ,중국,จีน,beijing,peking,shanghai,tianjin,chongqing,guangdong,canton,zhejiang,jiangsu,shandong,sichuan,hubei,hunan,fujian,anhui,henan,hebei,shanxi,shaanxi,liaoning,jilin,heilongjiang,jiangxi,guangxi,yunnan,guizhou,hainan,gansu,qinghai,ningxia,xinjiang,tibet,xizang,innermongolia,guangzhou,shenzhen,zhuhai,foshan,dongguan,zhongshan,huizhou,shantou,hangzhou,ningbo,wenzhou,shaoxing,jiaxing,suzhou,nanjing,wuxi,changzhou,xuzhou,nantong,jinan,qingdao,yantai,weihai,chengdu,mianyang,leshan,jiuzhaigou,wuhan,yichang,changsha,zhangjiajie,xiamen,fuzhou,quanzhou,hefei,huangshan,zhengzhou,luoyang,kaifeng,shijiazhuang,taiyuan,xi'an,xian,shenyang,dalian,changchun,harbin,nanchang,guilin,nanning,beihai,kunming,dali,lijiang,shangri-la,xishuangbanna,wuhua,guiyang,haikou,sanya,lanzhou,xining,yinchuan,urumqi,lhasa,hohhot,北京,上海,天津,重庆,重慶,广东,廣東,浙江,江苏,江蘇,山东,山東,四川,湖北,湖南,福建,安徽,河南,河北,山西,陕西,陝西,辽宁,遼寧,吉林,黑龙江,黑龍江,江西,广西,廣西,云南,雲南,贵州,貴州,海南,甘肃,青海,宁夏,新疆,西藏,内蒙古,广州,廣州,深圳,珠海,佛山,东莞,杭州,宁波,温州,苏州,蘇州,南京,无锡,济南,青岛,青島,武汉,武漢,长沙,長沙,厦门,廈門,福州,合肥,郑州,洛阳,西安,沈阳,大连,长春,哈尔滨,哈爾濱,南昌,桂林,南宁,昆明,大理,丽江,西双版纳,贵阳,海口,三亚,三亞,兰州,西宁,银川,乌鲁木齐,拉萨,ペキン,シャンハイ,広州,深セン,베이징,상하이,광저우,선전,항저우,청두,우한,시안,쿤밍,윈난",
    "@geo:cc:hk|@geo:cont:as:hongkong,hk,hkg,kowloon,tsimshatsui,causewaybay,lantau,香港,九龙,九龍,尖沙咀,铜锣湾,中环,香港島,ホンコン,홍콩",
    "@geo:cc:mo|@geo:cont:as:macau,macao,taipa,cotai,澳门,澳門,マカオ,마카오",
    "@geo:cc:tw|@geo:cont:as:taiwan,taiwanese,taipei,kaohsiung,taichung,tainan,hsinchu,hualien,台湾,台灣,台北,臺北,高雄,台中,台南,新竹,花莲,タイワン,대만,타이베이",
    "@geo:cc:jp|@geo:cont:as:japan,japanese,nippon,nihon,japon,giappone,日本,日本国,ジャパン,일본,ญี่ปุ่น,hokkaido,honshu,kyushu,shikoku,okinawa,kantō,kansai,tokyo,shinjuku,shibuya,ginza,akihabara,roppongi,asakusa,ueno,haneda,narita,yokohama,kanagawa,osaka,umeda,namba,nagoya,aichi,sapporo,otaru,hakodate,niseko,fukuoka,hakata,kyoto,arashiyama,gion,kobe,hyogo,kawasaki,saitama,hiroshima,sendai,miyagi,chiba,kitakyushu,sakai,niigata,hamamatsu,shizuoka,kumamoto,okayama,agano,nara,kanazawa,ishikawa,nagano,karuizawa,hakone,nikko,kamakura,fuji,naha,东京,東京,横滨,橫濱,大阪,名古屋,札幌,福冈,福岡,京都,神户,神戶,广岛,廣島,仙台,千叶,奈良,金泽,冲绳,沖繩,北海道,箱根,镰仓,富士山,トウキョウ,ヨコハマ,オオサカ,ナゴヤ,サッポロ,フクオカ,キョウト,コウベ,ヒロシマ,センダイ,オキナワ,도쿄,요코하마,오사카,나고야,삿포로,후쿠오카,교토,고베,히로시마,센다이,오키나와,홋카이도",
    "@geo:cc:kr|@geo:cont:as:korea,southkorea,korean,rok,corea,corée,südkorea,韩国,韓國,南韩,大韩民国,韓国,한국,대한민국,เกาหลีใต้,seoul,gangnam,hongdae,myeongdong,itaewon,busan,pusan,haeundae,incheon,daegu,daejeon,gwangju,suwon,ulsan,gyeonggi,jeju,jejudo,gyeongju,jeonju,pangyo,首尔,首爾,汉城,釜山,仁川,大邱,大田,光州,水原,蔚山,济州,濟州,济州岛,庆州,江南,明洞,弘大,ソウル,プサン,インチョン,チェジュ,済州島,서울,강남,명동,홍대,부산,해운대,인천,대구,대전,광주,수원,울산,제주,제주도,경주",
    "@geo:cc:sg|@geo:cont:as:singapore,singaporean,sgp,changi,sentosa,marinabay,新加坡,狮城,星洲,シンガポール,싱가포르,สิงคโปร์",
    "@geo:cc:my|@geo:cont:as:malaysia,malaysian,kualalumpur,penang,georgetown,johor,johorbahru,malacca,melaka,sabah,kotakinabalu,sarawak,kuching,langkawi,马来西亚,馬來西亞,大马,吉隆坡,槟城,檳城,新山,马六甲,沙巴,亚庇,兰卡威,マレーシア,クアラルンプール,말레이시아,쿠알라룸푸르,มาเลเซีย",
    "@geo:cc:th|@geo:cont:as:thailand,thai,siam,bangkok,suvarnabhumi,chiangmai,chiangrai,phuket,pattaya,krabi,samui,kohsamui,ayutthaya,huahin,泰国,泰國,曼谷,清迈,清邁,普吉,普吉岛,芭提雅,甲米,苏梅岛,大城,タイ,バンコク,チェンマイ,プーケット,태국,방콕,치앙마이,푸켓,ประเทศไทย,กรุงเทพ,เชียงใหม่,ภูเก็ต,pathum,sukhumvit,silom,chatuchak,巴吞旺,暹罗,素坤逸,是隆,乍都乍,サイアム,スクンビット,シーロム,チャトゥチャック,アユタヤ,クラビ,サムイ,パタヤ,시암,수쿰빗,실롬,짜뚜짝,아유타야,끄라비,사무이,파타야,ปทุมวัน,สยาม,สุขุมวิท,สีลม,จตุจักร,อยุธยา,กระบี่,สมุย,พัทยา",
    "@geo:cc:vn|@geo:cont:as:vietnam,vietnamese,hanoi,hochiminh,saigon,danang,nha-trang,nhatrang,hoian,halong,phuquoc,dalat,hue,越南,河内,河內,胡志明,胡志明市,西贡,岘港,芽庄,会安,下龙湾,富国岛,ベトナム,ハノイ,ホーチミン,ダナン,베트남,하노이,호치민,다낭,เวียดนาม",
    "@geo:cc:id|@geo:cont:as:indonesia,indonesian,jakarta,bali,denpasar,ubud,semiyak,kuta,surabaya,bandung,medan,yogyakarta,lombok,java,sumatra,印度尼西亚,印尼,雅加达,巴厘岛,峇里島,乌布,泗水,万隆,日惹,龙目岛,インドネシア,ジャカルタ,バリ島,인도네시아,자카르타,발리,อินโดนีเซีย",
    "@geo:cc:ph|@geo:cont:as:philippines,filipino,philippine,manila,makati,cebu,davao,boracay,palawan,bohol,luzon,菲律宾,菲律賓,马尼拉,馬尼拉,宿务,长滩岛,巴拉望,薄荷岛,フィリピン,マニラ,セブ,필리핀,마닐라,세부,ฟิลิปปินส์",
    "@geo:cc:kh|@geo:cont:as:cambodia,cambodian,khmer,phnompenh,siemreap,angkor,angkorwat,sihanoukville,柬埔寨,金边,金邊,暹粒,吴哥窟,西哈努克,カンボジア,プノンペン,アンコールワット,캄보디아,프놈펜,앙코르와트,กัมพูชา,កម្ពុជា,ភ្នំពេញ",
    "@geo:cc:la|@geo:cont:as:laos,lao,laotian,vientiane,luangprabang,vangvieng,老挝,寮國,万象,永珍,琅勃拉邦,万荣,ラオス,ビエンチャン,라오스,비엔티안,ลาว,ລາວ,ວຽງຈັນ",
    "@geo:cc:mm|@geo:cont:as:myanmar,burma,burmese,yangon,rangoon,mandalay,bagan,naypyidaw,缅甸,緬甸,仰光,曼德勒,蒲甘,内比都,ミャンマー,ヤンゴン,미얀마,양곤,พม่า",
    "@geo:cc:in|@geo:cont:as:india,indian,bharat,newdelhi,delhi,mumbai,bombay,bengaluru,bangalore,hyderabad,chennai,madras,kolkata,calcutta,pune,ahmedabad,jaipur,agra,goa,kerala,varanasi,印度,新德里,德里,孟买,孟買,班加罗尔,海得拉巴,金奈,加尔各答,浦那,斋浦尔,阿格拉,果阿,インド,ニューデリー,ムンバイ,バンガロール,인도,뉴델리,뭄바이,방갈로르,อินเดีย",
    "@geo:cc:pk|@geo:cont:as:pakistan,pakistani,islamabad,karachi,lahore,巴基斯坦,伊斯兰堡,卡拉奇,拉合尔,パキスタン,파키스탄",
    "@geo:cc:lk|@geo:cont:as:srilanka,ceylon,colombo,kandy,galle,斯里兰卡,科伦坡,康提,スリランカ,스리랑카",
    "@geo:cc:np|@geo:cont:as:nepal,nepali,kathmandu,pokhara,尼泊尔,加德满都,博卡拉,ネパール,네팔",
    "@geo:cc:ae|@geo:cont:as:uae,emirates,emirati,dubai,abudhabi,sharjah,阿联酋,阿聯酋,迪拜,杜拜,阿布扎比,阿布達比,沙迦,ドバイ,アブダビ,두바이,아부다비",
    "@geo:cc:sa|@geo:cont:as:saudiarabia,saudi,riyadh,jeddah,mecca,makkah,medina,dammam,沙特,沙特阿拉伯,沙烏地阿拉伯,利雅得,吉达,麦加,麦地那,サウジアラビア,リヤド,사우디아라비아,리야드",
    "@geo:cc:il|@geo:cont:as:israel,israeli,telaviv,jerusalem,haifa,eilat,以色列,特拉维夫,耶路撒冷,海法,イスラエル,テルアビブ,エルサレム,이스라엘,텔아비브,예루살렘",
    "@geo:cc:qa|@geo:cont:as:qatar,qatari,doha,卡塔尔,卡達,多哈,カタール,ドーハ,카타르,도하",
    "@geo:cc:jo|@geo:cont:as:jordan,jordanian,amman,petra,aqaba,deadsea,wadirum,约旦,約旦,安曼,佩特拉,阿卡巴,死海,瓦迪拉姆,ヨルダン,アンマン,ペトラ,요르단,암만,페트라",
    "@geo:cc:us|@geo:cont:am:usa,u.s.,u.s.a.,unitedstates,unitedstatesofamerica,美国,美國,美利坚,アメリカ合衆国,米国,미국,미합중국,สหรัฐอเมริกา,california,texas,florida,newyork,pennsylvania,illinois,ohio,georgia,northcarolina,michigan,newjersey,virginia,washington,arizona,massachusetts,tennessee,indiana,maryland,missouri,wisconsin,colorado,minnesota,southcarolina,alabama,louisiana,kentucky,oregon,oklahoma,connecticut,utah,iowa,nevada,arkansas,mississippi,kansas,newmexico,nebraska,idaho,hawaii,maine,newhampshire,rhodeisland,montana,delaware,southdakota,northdakota,alaska,vermont,wyoming,nyc,manhattan,brooklyn,queens,bronx,losangeles,hollywood,santamonica,beverlyhills,pasadena,irvine,chicago,houston,harriscounty,phoenix,scottsdale,tucson,philadelphia,pittsburgh,sanantonio,sandiego,la-jolla,dallas,fortworth,austin,sanjose,siliconvalley,sanfrancisco,berkeley,oakland,paloalto,mountainview,cupertino,sunnyvale,santaclara,stanford,jacksonville,columbus,charlotte,indianapolis,seattle,bellevue,redmond,tacoma,denver,boulder,washingtondc,d.c.,baltimore,boston,eastboston,suffolkcounty,midlesexcounty,middlesexcounty,harvard,mit,elpaso,nashville,memphis,detroit,annarbor,portland,lasvegas,reno,oklahomacity,louisville,milwaukee,madison,albuquerque,santafe,fresno,sacramento,kansascity,mesa,atlanta,omaha,coloradosprings,raleigh,durham,miami,orlando,tampa,keywest,minneapolis,st.paul,tulsa,cleveland,cincinnati,wichita,neworleans,orleansparish,storyville,kenner,jeffersonparish,batonrouge,honolulu,waikiki,maui,anchorage,saltlakecity,providence,newhaven,princeton,ithaca,加利福尼亚,加州,德克萨斯,德州,佛罗里达,佛州,纽约,紐約,宾夕法尼亚,伊利诺伊,俄亥俄,佐治亚,密歇根,新泽西,弗吉尼亚,华盛顿州,亚利桑那,马萨诸塞,麻省,路易斯安那,俄勒冈,科罗拉多,犹他,内华达,夏威夷,阿拉斯加,纽约市,曼哈顿,布鲁克林,洛杉矶,洛杉磯,好莱坞,芝加哥,休斯顿,休士頓,菲尼克斯,凤凰城,费城,圣安东尼奥,圣地亚哥,圣迭戈,达拉斯,奥斯汀,圣何塞,硅谷,旧金山,舊金山,三藩市,伯克利,帕罗奥图,西雅图,西雅圖,丹佛,华盛顿,華盛頓,巴尔的摩,波士顿,波士頓,底特律,波特兰,拉斯维加斯,迈阿密,邁阿密,奥兰多,亚特兰大,明尼阿波利斯,新奥尔良,紐奧良,檀香山,盐湖城,ニューヨーク,ロサンゼルス,シカゴ,ヒューストン,サンフランシスコ,シアトル,ボストン,ワシントン,ラスベガス,マイアミ,ニューオーリンズ,ハワイ,ホノルル,뉴욕,로스앤젤레스,시카고,휴스턴,샌프란시스코,시애틀,보스턴,워싱턴,라스베이거스,마이애미,뉴올리언스,하와이,호놀룰루,orleans,francisco,angeles,vegas,diego,jose,antonio,得克萨斯,ルイジアナ,カリフォルニア,テキサス,フロリダ,루이지애나,캘리포니아,텍사스,플로리다,kalifornien,californie,floride,นิวออร์ลีนส์,แคลิฟอร์เนีย,ฮาวาย",
    "@geo:cc:ca|@geo:cont:am:canada,canadian,kanada,canadá,加拿大,カナダ,캐나다,แคนาดา,ontario,quebec,québec,britishcolumbia,alberta,manitoba,saskatchewan,novascotia,toronto,montreal,montréal,vancouver,calgary,edmonton,ottawa,winnipeg,quebeccity,waterloo,banff,whistler,halifax,多伦多,多倫多,蒙特利尔,蒙特婁,温哥华,溫哥華,卡尔加里,埃德蒙顿,渥太华,渥太華,温尼伯,魁北克,班夫,滑铁卢,トロント,モントリオール,バンクーバー,オタワ,カルガリー,토론토,몬트리올,밴쿠버,오타와,캘거리",
    "@geo:cc:mx|@geo:cont:am:mexico,mexican,méxico,mexiko,mexique,墨西哥,メキシコ,멕시코,เม็กซิโก,mexicocity,cdmx,guadalajara,monterrey,puebla,tijuana,cancun,cancún,tulum,oaxaca,墨西哥城,坎昆,瓜达拉哈拉,蒙特雷,メキシコシティ,カンクン,멕시코시티,칸쿤",
    "@geo:cc:br|@geo:cont:am:brazil,brazilian,brasil,brasilien,brésil,巴西,ブラジル,브라질,บราซิล,saopaulo,sãopaulo,riodejaneiro,brasilia,brasília,salvador,fortaleza,belohorizonte,manaus,curitiba,recife,圣保罗,聖保羅,里约热内卢,里約熱內盧,巴西利亚,サンパウロ,リオデジャネイロ,상파울루,리우데자네이루",
    "@geo:cc:ar|@geo:cont:am:argentina,argentinian,argentine,argentinien,阿根廷,アルゼンチン,아르헨티나,buenosaires,rosario,mendoza,bariloche,patagonia,ushuaia,布宜诺斯艾利斯,ブエノスアイレス,부에노스아이레스",
    "@geo:cc:cl|@geo:cont:am:chile,chilean,chili,智利,チリ,칠레,valparaíso,atacama,智利圣地亚哥",
    "@geo:cc:co|@geo:cont:am:colombia,colombian,哥伦比亚,コロンビア,콜롬비아,bogota,bogotá,medellin,medellín,cartagena,波哥大,麦德林",
    "@geo:cc:pe|@geo:cont:am:peru,peruvian,perú,秘鲁,祕魯,ペルー,페루,lima,cusco,cuzco,machupicchu,利马,库斯科,马丘比丘",
    "@geo:cc:eg|@geo:cont:af:egypt,egyptian,ägypten,égypte,egipto,egitto,misr,埃及,エジプト,이집트,египет,อียิปต์,cairo,greatercairo,giza,alexandria,luxor,aswan,sharmelsheikh,hurghada,sinai,nile,karnak,pyramids,heliopolis,zamalek,maadi,开罗,開羅,吉萨,吉薩,亚历山大,卢克索,阿斯旺,沙姆沙伊赫,赫尔格达,尼罗河,金字塔,カイロ,ギザ,アレクサンドリア,ルクソール,アスワン,ナイル川,카이로,기자,알렉산드리아,룩소르,아스완,나일강,sharm,cataract,西奈,卡纳克,シナイ,ナイル,カルナック,시나이,나일,카르나크,kairo,nil,louxor,assouan,lúxor,asuán,nilo,ไคโร,กีซา,ลักซอร์,อัสวาน,แม่น้ำไนล์",
    "@geo:cc:za|@geo:cont:af:southafrica,southafrican,南非,南アフリカ,남아프리카,남아공,johannesburg,capetown,durban,pretoria,kruger,约翰内斯堡,开普敦,德班,比勒陀利亚,ヨハネスブルグ,ケープタウン,요하네스버그,케이프타운",
    "@geo:cc:ma|@geo:cont:af:morocco,moroccan,maroc,marokko,marruecos,摩洛哥,モロッコ,모로코,casablanca,marrakesh,marrakech,rabat,fez,fes,tangier,chefchaouen,卡萨布兰卡,马拉喀什,拉巴特,非斯,丹吉尔",
    "@geo:cc:ke|@geo:cont:af:kenya,kenyan,肯尼亚,肯亞,ケニア,케냐,nairobi,mombasa,masaimara,内罗毕,蒙巴萨,ナイロビ,나이로비",
    "@geo:cc:ng|@geo:cont:af:nigeria,nigerian,尼日利亚,ナイジェリア,나이지리아,lagos,abuja,拉各斯,阿布贾",
    "@geo:cc:et|@geo:cont:af:ethiopia,ethiopian,埃塞俄比亚,エチオピア,에티오피아,addisababa,亚的斯亚贝巴",
    "@geo:cc:tz|@geo:cont:af:tanzania,tanzanian,坦桑尼亚,タンザニア,탄자니아,daressalaam,zanzibar,serengeti,kilimanjaro,桑给巴尔,乞力马扎罗",
    "@geo:cc:au|@geo:cont:oc:australia,australian,aussie,australien,australie,澳大利亚,澳大利亞,澳洲,オーストラリア,豪州,호주,오스트레일리아,ออสเตรเลีย,nsw,newsouthwales,victoria,queensland,westernaustralia,southaustralia,tasmania,sydney,melbourne,brisbane,perth,adelaide,goldcoast,canberra,wollongong,hobart,darwin,cairns,悉尼,雪梨,墨尔本,墨爾本,布里斯班,珀斯,伯斯,阿德莱德,黄金海岸,堪培拉,坎培拉,霍巴特,达尔文,凯恩斯,塔斯马尼亚,シドニー,メルボルン,ブリスベン,パース,アデレード,キャンベラ,ゴールドコースト,시드니,멜버른,브리즈번,퍼스,애들레이드,캔버라,골드코스트",
    "@geo:cc:nz|@geo:cont:oc:newzealand,kiwi,aotearoa,neuseeland,nouvelle-zélande,新西兰,紐西蘭,ニュージーランド,뉴질랜드,นิวซีแลนด์,auckland,wellington,christchurch,queenstown,hamilton,dunedin,rotorua,奥克兰,奧克蘭,惠灵顿,威靈頓,基督城,皇后镇,罗托鲁瓦,オークランド,ウェリントン,クライストチャーチ,クイーンズタウン,오클랜드,웰링턴,크라이스트처치,퀸스타운",
    "@geo:cc:al|@geo:cont:eu:albania,albanian,shqipëri,tirana,tiranë,durrës,阿尔巴尼亚,地拉那,アルバニア,알바니아",
    "@geo:cc:ba|@geo:cont:eu:bosnia,herzegovina,bosnian,sarajevo,mostar,波黑,萨拉热窝,ボスニア,보스니아",
    "@geo:cc:mk|@geo:cont:eu:macedonia,northmacedonia,skopje,ohrid,北马其顿,斯科普里",
    "@geo:cc:me|@geo:cont:eu:montenegro,podgorica,kotor,budva,黑山,波德戈里察,科托尔",
    "@geo:cc:by|@geo:cont:eu:belarus,belarusian,minsk,gomel,白俄罗斯,明斯克,ベラルーシ,벨라루스",
    "@geo:cc:md|@geo:cont:eu:moldova,chișinău,chisinau,摩尔多瓦,基希讷乌",
    "@geo:cc:ad|@geo:cont:eu:andorra,安道尔,アンドラ,안도라",
    "@geo:cc:li|@geo:cont:eu:liechtenstein,vaduz,列支敦士登,瓦杜兹",
    "@geo:cc:sm|@geo:cont:eu:sanmarino,圣马力诺",
    "@geo:cc:va|@geo:cont:eu:vatican,vaticano,holysee,梵蒂冈,バチカン,바티칸",
    "@geo:cc:ge|@geo:cont:as:tbilisi,batumi,kutaisi,格鲁吉亚,第比利斯,ジョージア,조지아",
    "@geo:cc:am|@geo:cont:as:armenia,armenian,yerevan,亚美尼亚,埃里温,アルメニア,아르메니아",
    "@geo:cc:az|@geo:cont:as:azerbaijan,baku,阿塞拜疆,巴库,アゼルバイジャン,아제르바이잔",
    "@geo:cc:kz|@geo:cont:as:kazakhstan,kazakh,astana,almaty,shymkent,哈萨克斯坦,阿斯塔纳,阿拉木图,カザフスタン,카자흐스탄",
    "@geo:cc:uz|@geo:cont:as:uzbekistan,uzbek,tashkent,samarkand,bukhara,乌兹别克斯坦,塔什干,撒马尔罕,ウズベキスタン,우즈베키스탄",
    "@geo:cc:kg|@geo:cont:as:kyrgyzstan,bishkek,osh,吉尔吉斯斯坦,比什凯克",
    "@geo:cc:tj|@geo:cont:as:tajikistan,dushanbe,塔吉克斯坦,杜尚别",
    "@geo:cc:tm|@geo:cont:as:turkmenistan,ashgabat,土库曼斯坦,阿什哈巴德",
    "@geo:cc:mn|@geo:cont:as:mongolia,mongolian,ulaanbaatar,ulanbator,蒙古,蒙古国,乌兰巴托,モンゴル,ウランバートル,몽골,울란바토르",
    "@geo:cc:bd|@geo:cont:as:bangladesh,bangladeshi,dhaka,chittagong,孟加拉,孟加拉国,达卡,バングラデシュ,방글라데시",
    "@geo:cc:bt|@geo:cont:as:bhutan,thimphu,paro,不丹,廷布",
    "@geo:cc:mv|@geo:cont:as:maldives,maldivian,malé,马尔代夫,馬爾地夫,马累,モルディブ,몰디브",
    "@geo:cc:bn|@geo:cont:as:brunei,bandarseribegawan,文莱,汶萊,斯里巴加湾,ブルネイ,브루나이",
    "@geo:cc:kw|@geo:cont:as:kuwait,kuwaitcity,科威特,科威特城,クウェート,쿠웨이트",
    "@geo:cc:om|@geo:cont:as:oman,omani,muscat,salalah,阿曼,马斯喀特,オマーン,오만",
    "@geo:cc:bh|@geo:cont:as:bahrain,manama,巴林,麦纳麦,バーレーン,바레인",
    "@geo:cc:lb|@geo:cont:as:lebanon,lebanese,beirut,byblos,黎巴嫩,贝鲁特,レバノン,ベイルート,레바논,베이루트",
    "@geo:cc:ir|@geo:cont:as:iran,iranian,persia,persian,tehran,isfahan,shiraz,tabriz,伊朗,德黑兰,伊斯法罕,设拉子,イラン,テヘラン,이란,테헤란",
    "@geo:cc:iq|@geo:cont:as:iraq,iraqi,baghdad,basra,erbil,伊拉克,巴格达,イラク,バグダッド,이라크,바그다드",
    "@geo:cc:sy|@geo:cont:as:syria,syrian,damascus,aleppo,叙利亚,大马士革,シリア,시리아",
    "@geo:cc:af|@geo:cont:as:afghanistan,afghan,kabul,herat,阿富汗,喀布尔,アフガニスタン,아프가니스탄",
    "@geo:cc:cu|@geo:cont:am:cuba,cuban,havana,habana,varadero,古巴,哈瓦那,キューバ,ハバナ,쿠바,아바나",
    "@geo:cc:jm|@geo:cont:am:jamaica,jamaican,kingston,montegobay,牙买加,金斯敦,ジャマイカ,자메이카",
    "@geo:cc:cr|@geo:cont:am:costarica,sanjosé,哥斯达黎加,コスタリカ,코스타리카",
    "@geo:cc:pa|@geo:cont:am:panama,panamacity,巴拿马,巴拿马城,パナマ,파나마",
    "@geo:cc:uy|@geo:cont:am:uruguay,montevideo,puntadeleste,乌拉圭,蒙得维的亚,ウルグアイ,우루과이",
    "@geo:cc:ec|@geo:cont:am:ecuador,quito,guayaquil,galapagos,galápagos,厄瓜多尔,基多,加拉帕戈斯,エクアドル,에콰도르",
    "@geo:cc:bo|@geo:cont:am:bolivia,lapaz,sucre,uyuni,玻利维亚,拉巴斯,乌尤尼,ボリビア,볼리비아",
    "@geo:cc:ve|@geo:cont:am:venezuela,caracas,委内瑞拉,加拉加斯,ベネズエラ,베네수엘라",
    "@geo:cc:do|@geo:cont:am:dominicanrepublic,santodomingo,puntacana,多米尼加,圣多明各",
    "@geo:cc:pr|@geo:cont:am:puertorico,sanjuan,波多黎各,圣胡安",
    "@geo:cc:tn|@geo:cont:af:tunisia,tunisian,tunis,carthage,sousse,突尼斯,迦太基,チュニジア,튀니지",
    "@geo:cc:dz|@geo:cont:af:algeria,algerian,algiers,oran,阿尔及利亚,阿尔及尔,アルジェリア,알제리",
    "@geo:cc:gh|@geo:cont:af:ghana,ghanaian,accra,kumasi,加纳,阿克拉,ガーナ,가나",
    "@geo:cc:sn|@geo:cont:af:senegal,dakar,塞内加尔,达喀尔,セネガル,세네갈",
    "@geo:cc:rw|@geo:cont:af:rwanda,kigali,卢旺达,基加利,ルワンダ,르완다",
    "@geo:cc:mu|@geo:cont:af:mauritius,portlouis,毛里求斯,路易港,モーリシャス,모리셔스",
    "@geo:cc:ug|@geo:cont:af:uganda,kampala,entebbe,乌干达,坎帕拉",
    "@geo:cc:zw|@geo:cont:af:zimbabwe,harare,victoriafalls,津巴布韦,哈拉雷,维多利亚瀑布",
    "@geo:cc:na|@geo:cont:af:namibia,windhoek,纳米比亚,温得和克",
    "@geo:cc:bw|@geo:cont:af:botswana,gaborone,okavango,博茨瓦纳,哈博罗内",
    "@geo:cc:fj|@geo:cont:oc:fiji,fijian,suva,nadi,斐济,斐濟,苏瓦,楠迪,フィジー,피지",
    "@geo:cc:pg|@geo:cont:oc:papuanewguinea,portmoresby,巴布亚新几内亚,莫尔兹比港",
    "@geo:cc:pf|@geo:cont:oc:tahiti,borabora,frenchpolynesia,大溪地,塔希提,波拉波拉",
    "@kin:parent:mother|@kin:family:mom,mum,mommy,mummy,mama,mother,mothers,maternal,妈妈,媽媽,母亲,母親,老妈,老媽,娘亲,母上,岳母,婆婆,丈母娘,母,お母さん,ママ,母親,엄마,어머니,모친,mutter,mutti,maman,mere,mère,madre,mamá,แม่,มารดา",
    "@kin:parent:father|@kin:family:dad,daddy,papa,father,fathers,paternal,爸爸,父亲,父親,老爸,爹,父上,岳父,公公,老丈人,父,お父さん,パパ,父親,아빠,아버지,부친,vater,vati,père,padre,papá,พ่อ,บิดา",
    "@kin:parent|@kin:family:parent,parents,folks,双亲,雙親,父母,爸妈,爸媽,爹娘,両親,부모,부모님,eltern,padres",
    "@kin:spouse|@kin:family:wife,wives,husband,husbands,spouse,spouses,fiance,fiancé,fiancee,fiancée,newlywed,bride,groom,妻子,老婆,太太,夫人,媳妇,爱人,愛人,丈夫,老公,先生,伴侣,伴侶,配偶,未婚夫,未婚妻,新娘,新郎,妻,夫,嫁,旦那,奥さん,主人,配偶者,아내,남편,배우자,와이프,신부,신랑,ehefrau,ehemann,gattin,épouse,époux,mari,femme,esposo,esposa,ภรรยา,สามี",
    "@kin:sibling|@kin:family:brother,brothers,bro,sister,sisters,sis,sibling,siblings,twin,twins,哥哥,弟弟,姐姐,妹妹,兄长,兄弟,姐妹,姊妹,姐弟,兄妹,胞兄,胞弟,双胞胎,雙胞胎,兄,弟,姉,妹,お兄さん,お姉さん,兄弟,姉妹,형,오빠,동생,누나,언니,남동생,여동생,형제,자매,bruder,schwester,geschwister,frère,sœur,hermano,hermana,พี่ชาย,น้องชาย,พี่สาว,น้องสาว",
    "@kin:child|@kin:family:son,sons,daughter,daughters,child,children,kid,kids,toddler,infant,newborn,offspring,儿子,兒子,女儿,女兒,孩子,小孩,子女,儿女,兒女,宝宝,寶寶,婴儿,嬰兒,闺女,小子,息子,娘,子供,子ども,赤ちゃん,아들,딸,아이,자녀,아기,sohn,tochter,kinder,fils,fille,enfant,hijo,hija,niño,ลูกชาย,ลูกสาว,ลูก",
    "@kin:grandparent|@kin:family:grandpa,grandfather,granddad,grandma,grandmother,granny,nana,grandparent,grandparents,grandchild,grandchildren,grandson,granddaughter,爷爷,爺爺,奶奶,外公,外婆,姥爷,姥姥,祖父,祖母,外祖父,外祖母,孙子,孫子,孙女,外孙,外孙女,祖父,祖母,おじいさん,おばあさん,孫,할아버지,할머니,외할아버지,외할머니,손자,손녀,großvater,großmutter,oma,opa,grand-père,grand-mère,abuelo,abuela,ปู่,ย่า,ตา,ยาย",
    "@kin:extended|@kin:family:uncle,uncles,aunt,aunts,auntie,cousin,cousins,nephew,nephews,niece,nieces,in-law,in-laws,relative,relatives,kin,clan,叔叔,伯伯,伯父,叔父,舅舅,姑姑,姑妈,姨妈,阿姨,表哥,表姐,表弟,表妹,堂哥,堂姐,堂弟,堂妹,侄子,侄女,外甥,外甥女,亲戚,親戚,家人,家属,家庭,叔父,伯父,叔母,伯母,おじ,おば,いとこ,甥,姪,親戚,家族,삼촌,이모,고모,숙모,사촌,조카,친척,가족,onkel,tante,cousine,neffe,nichte,familie",
    "@role:colleague|@dim:social:colleague,colleagues,coworker,coworkers,co-worker,teammate,teammates,workmate,collaborator,coauthor,co-author,boss,manager,supervisor,intern,同事,同僚,队友,隊友,搭档,搭檔,合作者,合著者,上司,主管,经理,老板,老闆,实习生,同僚,上司,部下,先輩,後輩,동료,선배,후배,상사,팀원,kollege,kollegin,mitarbeiter,chef,collègue,compañero,เพื่อนร่วมงาน",
    "@role:academic|@dim:social:professor,prof,phd,postdoc,researcher,scientist,scholar,advisor,supervisor,mentor,student,classmate,schoolmate,alumni,dean,lecturer,教授,导师,導師,博导,博士,博士后,研究员,科学家,学者,学生,學生,同学,同學,校友,师兄,师姐,师弟,师妹,讲师,院长,教授,先生,研究者,学生,同級生,교수,박사,연구원,학생,동창,동문,선생님,professorin,dozent,étudiant,profesor,ศาสตราจารย์,นักศึกษา",
    "@role:medical|@dim:social:doctor,doctors,physician,surgeon,dentist,orthodontist,nurse,pharmacist,therapist,psychiatrist,psychologist,pediatrician,optometrist,vet,veterinarian,clinic,hospital,pharmacy,医生,醫生,大夫,医师,外科医生,牙医,牙醫,护士,護士,药剂师,心理医生,兽医,獸醫,诊所,診所,医院,醫院,药房,药店,医者,医師,歯医者,歯科医,看護師,病院,クリニック,薬局,의사,치과의사,간호사,병원,치과,약국,arzt,ärztin,zahnarzt,krankenhaus,apotheke,médecin,dentiste,hôpital,médico,หมอ,แพทย์,ทันตแพทย์,โรงพยาบาล,dental,tooth,teeth,periodontal,orthodontic,cavity,radiograph,surgery,surgical,checkup,牙科,牙齿,洗牙,洁牙,补牙,拔牙,牙周,正畸,手术,体检,歯科,歯,歯周病,手術,健診,치아,치주,수술,검진,zahnmedizin,zahn,zähne,zahnärztlich,dentaire,dent,dents,chirurgie,diente,dientes,cirugía,ทันตกรรม,ฟัน,ผ่าตัด,ตรวจสุขภาพ",
    "@role:friend|@dim:social:friend,friends,bestie,buddy,pal,roommate,flatmate,housemate,neighbor,neighbour,boyfriend,girlfriend,朋友,好友,闺蜜,閨蜜,死党,发小,室友,舍友,邻居,鄰居,男朋友,男友,女朋友,女友,友達,友人,親友,ルームメイト,隣人,彼氏,彼女,친구,절친,룸메이트,이웃,남자친구,여자친구,freund,freundin,mitbewohner,nachbar,ami,amie,amigo,เพื่อน,รูมเมท",
    "@cal:month:01|@cal:season:winter:january,jan,一月,1月,正月,元月,1월,일월,januar,janvier,enero,มกราคม",
    "@cal:month:02|@cal:season:winter:february,feb,二月,2月,2월,이월,februar,février,febrero,กุมภาพันธ์",
    "@cal:month:03|@cal:season:spring:march,mar,三月,3月,3월,삼월,märz,mars,marzo,มีนาคม",
    "@cal:month:04|@cal:season:spring:april,apr,四月,4月,4월,사월,avril,abril,เมษายน",
    "@cal:month:05|@cal:season:spring:五月,5月,5월,오월,mai,mayo,พฤษภาคม",
    "@cal:month:06|@cal:season:summer:june,jun,六月,6月,6월,유월,juni,juin,junio,มิถุนายน",
    "@cal:month:07|@cal:season:summer:july,jul,七月,7月,7월,칠월,juli,juillet,julio,กรกฎาคม",
    "@cal:month:08|@cal:season:summer:august,aug,八月,8月,8월,팔월,août,agosto,สิงหาคม",
    "@cal:month:09|@cal:season:autumn:september,sep,sept,九月,9月,9월,구월,septembre,septiembre,กันยายน",
    "@cal:month:10|@cal:season:autumn:october,oct,十月,10月,10월,시월,oktober,octobre,octubre,ตุลาคม",
    "@cal:month:11|@cal:season:autumn:november,nov,十一月,11月,11월,십일월,novembre,noviembre,พฤศจิกายน",
    "@cal:month:12|@cal:season:winter:december,dec,十二月,12月,腊月,12월,십이월,dezember,décembre,diciembre,ธันวาคม",
    "@cal:season:spring|@dim:time:spring,springtime,春天,春季,春日,开春,春,봄,frühling,printemps,primavera,ฤดูใบไม้ผลิ",
    "@cal:season:summer|@dim:time:summer,summertime,midsummer,夏天,夏季,夏日,盛夏,夏,여름,sommer,été,verano,ฤดูร้อน",
    "@cal:season:autumn|@dim:time:autumn,autumnal,秋天,秋季,秋日,金秋,秋,가을,herbst,automne,otoño,ฤดูใบไม้ร่วง",
    "@cal:season:winter|@dim:time:winter,wintertime,midwinter,冬天,冬季,冬日,寒冬,冬,겨울,hiver,invierno,ฤดูหนาว",
    "@cal:dow:weekday|@dim:time:monday,mon,tuesday,tue,tues,wednesday,wed,thursday,thu,thur,thurs,friday,fri,weekday,weekdays,周一,周二,周三,周四,周五,星期一,星期二,星期三,星期四,星期五,礼拜一,礼拜二,礼拜三,礼拜四,礼拜五,工作日,平日,月曜日,火曜日,水曜日,木曜日,金曜日,月曜,火曜,水曜,木曜,金曜,월요일,화요일,수요일,목요일,금요일,평일,montag,dienstag,mittwoch,donnerstag,freitag,lundi,mardi,mercredi,jeudi,vendredi,lunes,martes,miércoles,jueves,viernes",
    "@cal:dow:weekend|@dim:time:saturday,sat,sunday,sun,weekend,weekends,周六,周日,周天,星期六,星期日,星期天,礼拜六,礼拜日,礼拜天,周末,週末,双休日,土曜日,日曜日,土曜,日曜,週末,토요일,일요일,주말,samstag,sonntag,wochenende,samedi,dimanche,sábado,domingo,สุดสัปดาห์",
    "@cal:holiday|@dim:time:christmas,xmas,christmasday,christmaseve,easter,halloween,thanksgiving,newyear,newyears,valentine,valentines,圣诞,圣诞节,聖誕節,平安夜,复活节,万圣节,感恩节,元旦,新年,春节,春節,除夕,中秋,中秋节,端午,端午节,清明,国庆,国庆节,情人节,クリスマス,正月,大晦日,バレンタイン,ハロウィン,크리스마스,설날,추석,신정,weihnachten,ostern,silvester,noël,navidad",
    "@bio:animal:cat|@bio:pet:cat,cats,kitten,kittens,kitty,feline,tabby,shorthair,longhair,persian,siamese,ragdoll,maine-coon,calico,catgrass,猫,貓,猫咪,貓咪,小猫,幼猫,喵星人,橘猫,英短,美短,布偶猫,暹罗猫,猫草,猫粮,猫砂,ネコ,猫ちゃん,子猫,고양이,새끼고양이,냥이,katze,kätzchen,kater,chat,chaton,gato,gatito,แมว,ลูกแมว",
    "@bio:animal:dog|@bio:pet:dog,dogs,puppy,puppies,pup,canine,hound,retriever,goldenretriever,labrador,corgi,poodle,husky,bulldog,beagle,shepherd,chihuahua,shiba,dachshund,pug,terrier,collie,spaniel,狗,狗狗,小狗,幼犬,犬,汪星人,金毛,拉布拉多,柯基,泰迪,贵宾犬,哈士奇,柴犬,牧羊犬,德牧,边牧,法斗,比熊,博美,雪纳瑞,狗粮,遛狗,イヌ,犬,子犬,柴犬,개,강아지,멍멍이,반려견,시바견,골든리트리버,웰시코기,푸들,hund,welpe,chien,chiot,perro,cachorro,สุนัข,หมา,ลูกหมา",
    "@bio:pet|@dim:bio:pet,pets,hamster,guineapig,ferret,bunny,rabbit,rabbits,parrot,parakeets,goldfish,aquarium,leash,collar,litterbox,宠物,寵物,毛孩子,仓鼠,倉鼠,豚鼠,荷兰猪,兔子,小兔,鹦鹉,鸚鵡,金鱼,水族箱,鱼缸,牵引绳,猫窝,狗窝,ペット,うさぎ,ハムスター,インコ,金魚,반려동물,애완동물,토끼,햄스터,앵무새,금붕어,haustier,kaninchen,hamster,mascota,สัตว์เลี้ยง,กระต่าย",
    "@bio:animal:bovine|@dim:bio:cow,cows,cattle,calf,calves,bull,bulls,ox,oxen,bison,buffalo,yak,herd,herded,herding,pasture,grazing,dairy,牛,奶牛,黄牛,水牛,牦牛,小牛,牛群,放牛,牧场,牧場,草场,ウシ,牛,乳牛,和牛,소,젖소,한우,송아지,kuh,kühe,rind,rinder,kalb,stier,vache,bœuf,troupeau,vaca,toro,ganado,วัว,โค,ควาย",
    "@bio:animal:equine|@dim:bio:horse,horses,pony,ponies,stallion,mare,foal,donkey,mule,camel,camels,alpaca,llama,sheep,lamb,flock,goat,goats,pig,pigs,boar,马,馬,小马,骏马,骑马,驴,骆驼,駱駝,羊驼,羊,绵羊,山羊,小羊,羊群,猪,豬,野猪,ウマ,馬,羊,ヒツジ,ラクダ,ブタ,말,양,낙타,염소,돼지,pferd,schaf,ziege,kamel,schwein,cheval,mouton,chameau,caballo,oveja,ម้า,แกะ,อูฐ",
    "@bio:animal:wildlife|@dim:bio:bird,birds,pigeon,seagull,duck,ducks,goose,geese,swan,swans,eagle,owl,penguin,penguins,peacock,flamingo,chicken,rooster,hen,deer,stag,reindeer,squirrel,fox,wolf,bear,panda,koala,kangaroo,elephant,lion,tiger,monkey,ape,giraffe,zebra,hippo,rhino,whale,dolphin,shark,seal,turtle,tortoise,frog,snake,butterfly,bee,鸟,鳥,小鸟,鸽子,海鸥,鸭,鸭子,鹅,天鹅,老鹰,猫头鹰,企鹅,孔雀,火烈鸟,鸡,公鸡,母鸡,鹿,梅花鹿,驯鹿,松鼠,狐狸,狼,熊,熊猫,大熊猫,考拉,袋鼠,大象,狮子,老虎,猴子,长颈鹿,斑马,河马,犀牛,鲸鱼,海豚,鲨鱼,海豹,乌龟,海龟,青蛙,蝴蝶,蜜蜂,鳥,ハト,カモ,白鳥,ペンギン,シカ,リス,パンダ,ゾウ,ライオン,トラ,サル,クジラ,イルカ,새,비둘기,오리,백조,펭귄,사슴,다람쥐,판다,코끼리,사자,호랑이,원숭이,고래,돌고래,vogel,hirsch,bär,löwe,와일드라이프,นก,ช้าง,เสือ,ลิง",
    "@bio:plant|@dim:bio:tree,trees,forest,woods,pine,oak,maple,palm,cherryblossom,sakura,bamboo,flower,flowers,rose,roses,tulip,sunflower,lotus,orchid,lavender,grass,lawn,moss,mushroom,mushrooms,fungi,truffle,cactus,succulent,garden,botanical,树,樹,树木,森林,树林,松树,橡树,枫树,枫叶,棕榈树,樱花,櫻花,竹,竹子,花,鲜花,玫瑰,郁金香,向日葵,荷花,兰花,薰衣草,草,草坪,草地,蘑菇,菌子,野生菌,松露,仙人掌,多肉,花园,植物园,木,樹木,森,桜,さくら,竹,花,バラ,チューリップ,ひまわり,芝生,きのこ,キノコ,庭園,植物園,나무,숲,벚꽃,대나무,꽃,장미,튤립,해바라기,잔디,버섯,정원,식물원,baum,bäume,wald,blume,pilz,pilze,garten,arbre,fleur,champignon,jardin,árbol,flor,ต้นไม้,ดอกไม้,เห็ด",
    "@obj:transit:air|@dim:travel:flight,flights,airplane,aeroplane,aircraft,airliner,plane,planes,jet,aviation,airline,airlines,airways,airport,airports,terminal,runway,tarmac,boarding,boardingpass,gate,baggage,luggage,carousel,layover,stopover,cockpit,cabin,inflight,cathay,lufthansa,ryanair,easyjet,delta,unitedairlines,britishairways,airfrance,emirates,航班,飞机,飛機,客机,航空,航空公司,机场,機場,航站楼,跑道,登机,登机牌,登机口,行李,行李转盘,转机,经停,机舱,头等舱,商务舱,经济舱,机票,飛行機,航空機,フライト,空港,ターミナル,滑走路,搭乗,搭乗券,手荷物,항공기,비행기,항공편,공항,터미널,활주로,탑승,탑승권,수하물,flug,flugzeug,flughafen,fluggesellschaft,gepäck,vol,avion,aéroport,bagage,vuelo,avión,aeropuerto,เที่ยวบิน,เครื่องบิน,สนามบิน",
    "@obj:transit:rail|@dim:travel:train,trains,railway,railroad,rail,locomotive,carriage,coach,platform,station,trainstation,highspeedrail,shinkansen,eurostar,tgv,amtrak,subway,underground,tube,metro,tram,streetcar,trolley,lightrail,火车,火車,列车,列車,高铁,高鐵,动车,動車,新干线,铁路,鐵路,火车站,站台,月台,车厢,车票,火车票,有轨电车,电车,轻轨,電車,列車,鉄道,新幹線,駅,ホーム,地下鉄,路面電車,기차,열차,고속열차,KTX,철도,기차역,지하철,전철,트램,zug,bahn,bahnhof,straßenbahn,u-bahn,s-bahn,gare,métro,tramway,tren,estación,รถไฟ,สถานีรถไฟ,รถไฟฟ้า",
    "@obj:transit:road|@dim:travel:taxi,taxis,cab,cabs,uber,lyft,didi,grab,bolt,rideshare,car,cars,automobile,sedan,suv,van,minivan,truck,lorry,bus,buses,coach,shuttle,motorcoach,driver,chauffeur,parking,garage,gasstation,petrol,toll,highway,freeway,motorway,traffic,出租车,出租車,的士,计程车,計程車,网约车,打车,打的,滴滴,优步,汽车,汽車,轿车,小车,越野车,面包车,卡车,货车,公交,公交车,巴士,大巴,班车,摆渡车,司机,停车场,加油站,油泵,高速,高速公路,堵车,タクシー,車,自動車,バス,駐車場,ガソリンスタンド,高速道路,택시,자동차,승용차,버스,셔틀,주차장,주유소,고속도로,auto,wagen,parkplatz,tankstelle,autobahn,voiture,station-service,autoroute,coche,autobús,รถแท็กซี่,รถยนต์,รถบัส",
    "@obj:transit:bike|@dim:travel:bicycle,bicycles,bike,bikes,cycling,cyclist,scooter,e-bike,ebike,motorbike,motorcycle,moped,helmet,handlebar,pedal,tire,tyre,wheel,自行车,自行車,单车,單車,脚踏车,共享单车,骑行,骑车,滑板车,电动车,电瓶车,摩托车,摩天轮,车轮,轮胎,头盔,修车,自転車,サイクリング,バイク,スクーター,자전거,사이클,오토바이,스쿠터,fahrrad,rad,motorrad,vélo,bicyclette,moto,bicicleta,จักรยาน,มอเตอร์ไซค์",
    "@obj:transit:marine|@dim:travel:ship,ships,boat,boats,ferry,cruise,cruiseship,yacht,sailboat,vessel,harbor,harbour,port,pier,dock,wharf,marina,deck,voyage,船,轮船,游轮,邮轮,郵輪,渡轮,游艇,帆船,小船,港口,码头,碼頭,甲板,游船,船,フェリー,クルーズ,ヨット,港,배,선박,유람선,크루즈,페리,요트,항구,부두,schiff,boot,fähre,kreuzfahrt,hafen,bateau,croisière,port,barco,crucero,เรือ,ท่าเรือ",
    "@venue:lodging|@dim:travel:hotel,hotels,inn,resort,resorts,hostel,motel,lodge,chalet,villa,guesthouse,b&b,airbnb,accommodation,accommodations,lodging,booking,reservation,check-in,checkin,checkout,check-out,suite,bedroom,lobby,reception,concierge,balcony,terrace,hilton,marriott,sheraton,hyatt,intercontinental,novotel,sofitel,ibis,westin,ritz,four-seasons,holidayinn,crowne,酒店,宾馆,賓館,旅馆,旅館,旅店,度假村,民宿,青旅,客栈,住宿,下榻,预订,預訂,入住,退房,套房,客房,房间,大堂,前台,阳台,陽台,露台,希尔顿,万豪,喜来登,凯悦,洲际,诺富特,索菲特,ホテル,旅館,宿,宿泊,予約,チェックイン,客室,バルコニー,호텔,숙소,숙박,리조트,펜션,민박,예약,체크인,객실,발코니,unterkunft,reservierung,zimmer,balkon,hôtel,hébergement,chambre,alojamiento,habitación,โรงแรม,ที่พัก,รีสอร์ท",
    "@venue:dining|@dim:food:restaurant,restaurants,bistro,brasserie,trattoria,izakaya,diner,eatery,canteen,cafeteria,foodcourt,cafe,café,coffeehouse,coffeeshop,teahouse,bakery,patisserie,pizzeria,steakhouse,pub,tavern,lounge,nightclub,餐厅,餐廳,饭店,飯店,餐馆,餐館,酒楼,食堂,茶餐厅,居酒屋,小馆,咖啡馆,咖啡厅,咖啡店,茶馆,奶茶店,面包店,烘焙店,甜品店,披萨店,牛排馆,酒吧,酒馆,夜市,レストラン,食堂,居酒屋,カフェ,喫茶店,パン屋,バー,식당,레스토랑,음식점,카페,커피숍,베이커리,빵집,술집,바,gaststätte,kneipe,bäckerei,boulangerie,restaurante,cafetería,ร้านอาหาร,คาเฟ่",
    "@obj:meal|@dim:food:breakfast,brunch,lunch,dinner,supper,banquet,feast,buffet,picnic,barbecue,bbq,takeout,takeaway,delivery,menu,appetizer,starter,entree,maincourse,side-dish,beverage,drink,drinks,早餐,早饭,早茶,早午餐,午餐,午饭,中餐,晚餐,晚饭,晚宴,聚餐,宵夜,夜宵,自助餐,野餐,烧烤,外卖,菜单,菜單,点菜,饮料,饮品,朝食,朝ご飯,昼食,昼ご飯,ランチ,夕食,晩ご飯,ディナー,ビュッフェ,メニュー,아침식사,점심,점심식사,저녁,저녁식사,만찬,뷔페,메뉴,배달,frühstück,mittagessen,abendessen,speisekarte,petit-déjeuner,déjeuner,dîner,desayuno,almuerzo,cena,อาหารเช้า,อาหารกลางวัน,อาหารเย็น",
    "@obj:food:dish|@dim:food:pizza,pizzas,pasta,spaghetti,ramen,noodle,noodles,udon,soba,sushi,sashimi,tempura,rice,friedrice,risotto,curry,steak,beef,pork,lamb,chicken,fish,seafood,shrimp,prawn,crab,lobster,octopus,squid,oyster,salmon,tuna,soup,hotpot,steampot,dumpling,dumplings,burger,hamburger,sandwich,toast,bread,croissant,salad,cheese,halloumi,egg,omelette,tofu,jam,tomatojam,dessert,desserts,cake,cakes,pastry,pastries,pie,tart,cookie,biscuit,chocolate,icecream,gelato,pudding,mousse,mochi,cream,waffle,pancake,coffee,espresso,latte,cappuccino,tea,milktea,juice,wine,beer,cocktail,whiskey,披萨,比萨,意面,拉面,面条,乌冬面,寿司,壽司,刺身,生鱼片,天妇罗,米饭,炒饭,咖喱,牛排,牛肉,猪肉,羊肉,鸡肉,鱼,海鲜,虾,螃蟹,龙虾,章鱼,鱿鱼,三文鱼,金枪鱼,汤,火锅,火้อ,汽锅,汽锅鸡,菌菇锅,饺子,汉堡,三明治,吐司,面包,牛角包,沙拉,奶酪,芝士,鸡蛋,豆腐,果酱,甜品,甜点,甜點,蛋糕,糕点,饼干,巧克力,冰淇淋,雪糕,布丁,慕斯,麻薯,糯米糍,奶油,华夫饼,松饼,咖啡,拿铁,茶,奶茶,果汁,红酒,葡萄酒,啤酒,鸡尾酒,ピザ,パスタ,ラーメン,うどん,寿司,刺身,ご飯,カレー,ステーキ,海鮮,鍋,餃子,パン,サラダ,チーズ,デザート,ケーキ,アイスクリーム,餅,モチ,コーヒー,お茶,ビール,ワイン,피자,파스타,라면,국수,우동,초밥,스시,사시미,회,밥,카레,스테이크,해산물,전골,훠궈,만두,햄버거,샌드위치,빵,샐러드,치즈,디저트,케이크,아이스크림,모찌,떡,커피,차,맥주,와인,mussel,mussels,scallop,scallops,clam,clams,crepe,crepes,danish,fennel,stew,stewed,charcuterie,chips,fries,kibble,青口,贻贝,扇贝,带子,蛤蜊,可丽饼,丹麦酥,炖菜,薯条,ムール貝,ホタテ,アサリ,クレープ,デニッシュ,シチュー,フライドポテト,홍합,가리비,조개,크레페,스튜,감자튀김,muscheln,jakobsmuscheln,eintopf,pommes,moules,coquilles,crêpe,ragoût,frites,mejillones,vieiras,estofado,papas,หอยแมลงภู่,หอยเชลล์,สตูว์,มันฝรั่งทอด",
    "@obj:doc:receipt|@dim:money:invoice,invoices,receipt,receipts,bill,bills,folio,statement,voucher,coupon,ticket,tickets,fare,fee,tuition,tax,vat,tip,gratuity,payment,paid,pay,charge,charged,cost,costs,price,prices,total,subtotal,amount,balance,refund,reimbursement,reimburse,expense,expenses,budget,salary,wage,rent,mortgage,deposit,transfer,transaction,creditcard,debitcard,visa,mastercard,amex,cash,atm,wallet,bank,发票,發票,收据,收據,账单,賬單,水单,结账单,小票,凭证,代金券,优惠券,票,门票,车票,机票,费用,学费,税,小费,付款,支付,结账,买单,收费,扣款,价格,价钱,花费,花销,开销,总计,总额,合计,余额,退款,报销,報銷,差旅费,预算,工资,薪水,房租,租金,押金,转账,交易,信用卡,刷卡,银行卡,现金,钱包,银行,領収書,レシート,請求書,明細,チケット,料金,代金,支払い,決済,費用,合計,返金,精算,クレジットカード,現金,銀行,영수증,청구서,명세서,계산서,티켓,요금,비용,결제,지불,가격,합계,총액,환불,정산,신용카드,현금,은행,rechnung,quittung,beleg,zahlung,preis,kosten,gesamt,kreditkarte,bargeld,facture,reçu,paiement,prix,factura,recibo,pago,precio,ใบเสร็จ,ใบแจ้งหนี้,ชำระเงิน,ราคา,spend,spends,spent,spending,reimbursing,消费,支出,花钱,出費,経費,지출,소비,경비,ausgeben,ausgaben,erstattung,dépenser,dépense,dépenses,remboursement,gastar,gasto,gastos,reembolso,ใช้จ่าย,ค่าใช้จ่าย,เบิกจ่าย",
    "@unit:curr:usd|@dim:money:usd,us$,$,dollar,dollars,buck,bucks,美元,美金,米ドル,ドル,미국달러,달러,us-dollar,dólar,ดอลลาร์,ดอลลาร์สหรัฐ",
    "@unit:curr:eur|@dim:money:eur,€,euro,euros,欧元,歐元,ユーロ,유로,ยูโร",
    "@unit:curr:gbp|@dim:money:gbp,£,pound,pounds,sterling,英镑,英鎊,ポンド,영국파운드,파운드,pfund,livre,libra,ปอนด์",
    "@unit:curr:chf|@dim:money:chf,franc,francs,瑞郎,瑞士法郎,スイスフラン,스위스프랑,schweizerfranken,francsuisse,francosuizo,ฟรังก์สวิส",
    "@unit:curr:cny|@dim:money:cny,rmb,¥,￥,yuan,人民币,人民幣,人民元,위안,런민비,renminbi,หยวน",
    "@unit:curr:jpy|@dim:money:jpy,yen,日元,円,엔,日圓,엔화,yens,เยน",
    "@unit:curr:hkd|@dim:money:hkd,港币,港元,港幣,香港ドル,홍콩달러,ดอลลาร์ฮ่องกง",
    "@unit:curr:aud|@dim:money:aud,a$",
    "@unit:curr:cad|@dim:money:cad,c$",
    "@unit:curr:sgd|@dim:money:sgd,s$",
    "@unit:curr:nzd|@dim:money:nzd",
    "@unit:curr:krw|@dim:money:krw,₩,won,韩元,韓元,韩币,ウォン,韓国ウォン,원,원화,วอน",
    "@unit:curr:inr|@dim:money:inr,₹",
    "@unit:curr:brl|@dim:money:brl,r$",
    "@unit:curr:rub|@dim:money:₽",
    "@unit:curr:mxn|@dim:money:mxn",
    "@unit:curr:sek|@dim:money:sek",
    "@unit:curr:nok|@dim:money:nok",
    "@unit:curr:dkk|@dim:money:dkk",
    "@unit:curr:pln|@dim:money:pln",
    "@unit:curr:czk|@dim:money:czk",
    "@unit:curr:huf|@dim:money:huf",
    "@unit:curr:try|@dim:money:₺",
    "@unit:curr:zar|@dim:money:zar",
    "@unit:curr:aed|@dim:money:aed",
    "@unit:curr:sar|@dim:money:sar",
    "@unit:curr:ils|@dim:money:ils,₪",
    "@unit:curr:twd|@dim:money:twd,nt$",
    "@unit:curr:thb|@dim:money:thb,฿,บาท,baht,泰铢,泰銖,バーツ,タイバーツ,바트,태국바트",
    "@unit:curr:idr|@dim:money:idr",
    "@unit:curr:myr|@dim:money:myr",
    "@unit:curr:php|@dim:money:php,₱",
    "@unit:curr:vnd|@dim:money:vnd,₫",
    "@unit:curr:lak|@dim:money:lak,ກີບ",
    "@unit:curr:khr|@dim:money:khr",
    "@unit:time:ns|@dim:duration:ns,nanosecond,nanoseconds,纳秒,奈秒,ナノ秒,나노초,nanosekunde,nanoseconde,nanosegundo,นาโนวินาที",
    "@unit:time:us|@dim:duration:µs,μs,microsecond,microseconds,微秒,マイクロ秒,마이크로초,mikrosekunde,microseconde,microsegundo,ไมโครวินาที",
    "@unit:time:ms|@dim:duration:ms,msec,millisecond,milliseconds,毫秒,ミリ秒,밀리초,millisekunde,milliseconde,milisegundo,มิลลิวินาที",
    "@unit:time:s|@dim:duration:s,sec,secs,second,seconds,秒,秒钟,초,sekunde,sekunden,วินาที,ວິນາທີ,វិនាទី,秒鐘,seconde,secondes,segundo,segundos",
    "@unit:time:min|@dim:duration:min,mins,minute,minutes,分钟,分,분,minuten,นาที,ນາທີ,នាទី,分鐘,minuto,minutos",
    "@unit:time:h|@dim:duration:h,hr,hrs,hour,hours,小时,钟头,時間,시간,stunde,stunden,ชั่วโมง,ຊົ່ວໂມງ,ម៉ោង,小時,鐘頭,heure,heures,hora,horas",
    "@unit:time:week|@dim:duration:wk,wks,week,weeks,星期,礼拜,週間,주간,woche,wochen,สัปดาห์,ອາທິດ,សប្តាហ៍,周,禮拜,주,semaine,semaines,semana,semanas",
    "@unit:time:day|@dim:duration:day,days,天,日,日間,일,tag,tage,tagen,วัน,ມື້,ថ្ងៃ,jour,jours,journée,día,días",
    "@unit:time:month|@dim:duration:month,months,月,月份,ヶ月,か月,월,monat,monate,monaten,เดือน,ເດືອນ,ខែ,개월,mois,mes,meses",
    "@unit:time:year|@dim:duration:year,years,yr,yrs,年,年份,年間,년,jahr,jahre,jahren,ปี,ປີ,ឆ្នាំ,an,ans,année,années,año,años",
    "@unit:time:quarter|@dim:duration:quarter,quarters,qtr,季度",
    "@unit:time:decade|@dim:duration:decade,decades,十年,年代",
    "@unit:time:fortnight|@dim:duration:fortnight,fortnights",
    "@unit:mass:mg|@dim:mass:mg,milligram,milligrams,毫克,ミリグラム,밀리그램,milligramm,milligramme,miligramo,มิลลิกรัม",
    "@unit:mass:g|@dim:mass:g,gram,grams,克,グラム,그램,gramm,กรัม,ກຣາມ,ក្រាម,gramme,gramo,gramos",
    "@unit:mass:kg|@dim:mass:kg,kilo,kilos,kilogram,kilograms,千克,公斤,キロ,キログラム,킬로그램,kilogramm,กิโลกรัม,ກິໂລກຣາມ,គីឡូក្រាម,킬로,kilogramme,kilogramo,กิโล",
    "@unit:mass:ton|@dim:mass:t,ton,tons,tonne,tonnes,吨,噸,公吨,トン,톤,tonnen,tonelada,toneladas,ตัน",
    "@unit:mass:lb|@dim:mass:lb,lbs,磅,ポンド,파운드,pfund,livre,libra,libras,ปอนด์",
    "@unit:mass:oz|@dim:mass:oz,ounce,ounces,盎司,オンス,온스,unze,unzen,once,onza,onzas,ออนซ์",
    "@unit:len:mm|@dim:length:mm,millimeter,millimeters,millimetre,毫米,公厘,ミリ,ミリメートル,밀리미터,millimètre,milímetro,มิลลิเมตร",
    "@unit:len:cm|@dim:length:cm,centimeter,centimeters,centimetre,厘米,公分,เซนติเมตร,センチ,センチメートル,센티미터,zentimeter,centimètre,centímetro",
    "@unit:len:m|@dim:length:m,meter,meters,metre,metres,米,公尺,เมตร,ແມັດ,ម៉ែត្រ,メートル,미터,mètre,metro,metros",
    "@unit:len:km|@dim:length:km,kilometer,kilometers,kilometre,kilometres,千米,公里,キロメートル,킬로미터,kilometern,กิโลเมตร,ກິໂລແມັດ,គីឡូម៉ែត្រ,kilomètre,kilomètres,kilómetro,kilómetros",
    "@unit:len:ft|@dim:length:ft,foot,feet,英尺,呎,フィート,피트,fuß,pied,pieds,pie,pies,ฟุต",
    "@unit:len:mile|@dim:length:mi,mile,miles,英里,哩,マイル,마일,meile,meilen,mille,milla,millas,ไมล์",
    "@unit:vol:ml|@dim:volume:ml,milliliter,milliliters,millilitre,毫升,มิลลิลิตร,ミリリットル,밀리리터,mililitro",
    "@unit:vol:l|@dim:volume:l,liter,liters,litre,litres,升,公升,ลิตร,ລິດ,លីត្រ,リットル,리터,litro,litros",
    "@unit:vol:gal|@dim:volume:gal,gallon,gallons,加仑,加侖,ガロン,갤런,gallone,galón,galones,แกลลอน",
    "@unit:vol:pint|@dim:volume:pint,pints,品脱",
    "@unit:vol:quart|@dim:volume:quart,quarts,夸脱",
    "@unit:vol:tbsp|@dim:volume:tbsp,tablespoon,tablespoons,汤匙,大匙",
    "@unit:vol:tsp|@dim:volume:tsp,teaspoon,teaspoons,茶匙,小匙",
    "@unit:vol:floz|@dim:volume:floz",
    "@unit:data:kb|@dim:data:kb,kib,kilobyte,kilobytes,千字节",
    "@unit:data:mb|@dim:data:mb,mib,megabyte,megabytes,兆字节",
    "@unit:data:gb|@dim:data:gb,gib,gigabyte,gigabytes,吉字节",
    "@unit:data:tb|@dim:data:tb,tib,terabyte,terabytes,太字节",
    "@unit:data:pb|@dim:data:pb,pib,petabyte,petabytes,拍字节",
    "@unit:data:byte|@dim:data:byte,bytes,字节",
    "@unit:rate:bps|@dim:rate:bps",
    "@unit:rate:kbps|@dim:rate:kbps",
    "@unit:rate:mbps|@dim:rate:mbps",
    "@unit:rate:gbps|@dim:rate:gbps",
    "@unit:rate:qps|@dim:rate:qps",
    "@unit:rate:rps|@dim:rate:rps",
    "@unit:rate:tps|@dim:rate:tps",
    "@unit:freq:hz|@dim:freq:hz,khz,mhz,ghz,赫兹",
    "@unit:freq:rpm|@dim:freq:rpm",
    "@unit:temp:deg|@dim:temp:degc,degf,kelvin,℃,celsius,fahrenheit,摄氏度,华氏度,攝氏度,華氏度,摂氏,華氏,섭씨,화씨,grad,degré,degrés,grado,grados,องศาเซลเซียส,องศาฟาเรนไฮต์",
    "@unit:stat:pct|@dim:stat:%,pct,percent,percentage,percentages,百分比,百分点,prozent,เปอร์เซ็นต์,ເປີເຊັນ,ភាគរយ",
    "@unit:stat:bp|@dim:stat:bp,基点",
    "@unit:stat:quantile|@dim:stat:p50,p90,p95,p99,p999,percentile,quantile,median,中位数,分位数",
    "@unit:stat:mean|@dim:stat:avg,average,mean,平均值,均值",
    "@unit:stat:std|@dim:stat:std,stddev,stdev,variance,sigma,标准差,方差",
    "@unit:stat:ratio|@dim:stat:ppm,ppb",
    "@unit:stat:extreme|@dim:stat:max,maximum,minimum,最大值,最小值",
    "@unit:stat:total|@dim:stat:sum,total,总和,合计",
    "@unit:energy:j|@dim:energy:joule,joules,焦耳",
    "@unit:energy:kj|@dim:energy:kj,mj,kilojoule,kilojoules,千焦",
    "@unit:energy:cal|@dim:energy:cal,calorie,calories,卡路里,カロリー,칼로리,kalorie,kalorien,caloría,calorías,แคลอรี",
    "@unit:energy:kcal|@dim:energy:kcal,kilocalorie,kilocalories,千卡,大卡,キロカロリー,킬로칼로리,kilokalorie,kilocaloría,กิโลแคลอรี",
    "@unit:energy:wh|@dim:energy:wh",
    "@unit:energy:kwh|@dim:energy:kwh,mwh,watt-hour,watt-hours,kilowatt-hour,kilowatt-hours,瓦时,千瓦时,度电,千瓦時,度電,キロワット時,킬로와트시,kilowattheure,kilovatio-hora,กิโลวัตต์ชั่วโมง",
    "@unit:energy:ev|@dim:energy:ev",
    "@unit:power:w|@dim:power:watt,watts,瓦,瓦特,ワット,와트,vatios,วัตต์",
    "@unit:power:kw|@dim:power:kw,mw,gw,kilowatt,kilowatts,megawatt,megawatts,gigawatt,gigawatts,千瓦,兆瓦,kilowattstunde,kilowattstunden,กิโลวัตต์,キロワット,メガワット,킬로와트,메가와트,kilovatios",
    "@unit:power:hp|@dim:power:hp,horsepower,马力",
    "@unit:press:pa|@dim:pressure:kpa,mpa,hpa,pascal,pascals,kilopascal,kilopascals,帕,帕斯卡,千帕,兆帕",
    "@unit:press:psi|@dim:pressure:psi,mmhg,torr,mbar,millibar,millibars",
    "@unit:speed:mph|@dim:speed:mph",
    "@unit:speed:kmh|@dim:speed:kmh",
    "@unit:speed:kph|@dim:speed:kph",
    "@unit:speed:mps|@dim:speed:mps",
    "@unit:speed:kn|@dim:speed:knot,knots",
    "@unit:area:m2|@dim:area:m2,m²,sqm,平方米,平米,平方公尺,平方メートル,제곱미터,평방미터,quadratmeter,mètrecarré,metrocuadrado,ตารางเมตร",
    "@unit:area:km2|@dim:area:km2,km²,平方公里,平方千米,平方キロメートル,제곱킬로미터,quadratkilometer,kilomètrecarré,kilómetrocuadrado,ตารางกิโลเมตร",
    "@unit:area:cm2|@dim:area:cm2,cm²",
    "@unit:area:sqft|@dim:area:sqft,ft2,ft²,平方英尺",
    "@unit:area:ha|@dim:area:hectare,hectares,公顷,公頃,ヘクタール,헥타르,hektar,hectárea,hectáreas,เฮกตาร์",
    "@unit:area:acre|@dim:area:acre,acres,英亩",
    "@unit:volt:v|@dim:electric:volt,volts,mv,kv,kilovolt,kilovolts,伏,伏特,千伏",
    "@unit:amp:a|@dim:electric:amp,amps,ampere,amperes,milliampere,milliamperes,安培,毫安",
    "@unit:charge:ah|@dim:electric:mah",
    "@unit:res:ohm|@dim:electric:ohm,ohms,ω,欧姆",
    "@unit:power:va|@dim:electric:kva",
    "@unit:angle:deg|@dim:angle:deg,rad,radian,radians,องศา",
    "@unit:len:inch|@dim:length:inch,inches,英寸,吋,インチ,인치,zoll,pouce,pouces,pulgada,pulgadas,นิ้ว",
    "@bio:weather|@dim:nature:weather,forecast,meteorology,downpour,drizzle,blizzard,hail,hailing,hailstorm,hailstone,sleet,storm,storms,stormy,thunderstorm,thunder,lightning,typhoon,hurricane,tornado,天气,气象,天气预报,暴雨,暴风雪,冰雹,下冰雹,雷雨,雷暴,暴风雨,风暴,打雷,闪电,台风,飓风,龙卷风,天気,天候,気象,豪雨,吹雪,雹,ひょう,霰,嵐,暴風雨,稲妻,台風,ハリケーン,竜巻,날씨,기상,일기예보,폭우,폭설,눈보라,우박,진눈깨비,폭풍,폭풍우,천둥,번개,뇌우,태풍,허리케인,토네이도,wetter,wettervorhersage,wolkenbruch,schneesturm,hagel,hageln,hagelsturm,graupel,sturm,stürmisch,unwetter,gewitter,donner,blitz,taifun,hurrikan,météo,averse,grêle,grêler,giboulée,orage,orageux,tempête,tonnerre,éclair,foudre,typhon,ouragan,clima,pronóstico,aguacero,ventisca,granizo,granizar,granizada,aguanieve,tormenta,tormentoso,trueno,relámpago,tifón,huracán,สภาพอากาศ,พยากรณ์อากาศ,พายุฝน,พายุหิมะ,ลูกเห็บ,พายุลูกเห็บ,พายุ,พายุฝนฟ้าคะนอง,ฟ้าร้อง,ฟ้าผ่า,ไต้ฝุ่น,เฮอริเคน,ทอร์นาโด",

)
_ISO3166_GEO_MAP: Optional[Dict[str, List[Tuple[str, str]]]] = None


def _closed_set_axes(token: str) -> List[Tuple[str, str]]:
    """Collect, per token, every (tag1, tag2) axis of the merged closed-set table in entry order
    with exact-pair de-duplication (a token may own several axes; geographic entries head the table)."""
    global _ISO3166_GEO_MAP
    if _ISO3166_GEO_MAP is None:
        m: Dict[str, List[Tuple[str, str]]] = {}
        for entry in _ISO3166_GEO_ENTRIES:
            head, _, tail = entry.rpartition(":")
            t1, _, t2 = head.partition("|")
            pair = (t1, t2)
            for tok in tail.split(","):
                if tok:
                    axes = m.setdefault(tok, [])
                    if pair not in axes:
                        axes.append(pair)
        _ISO3166_GEO_MAP = m
    return _ISO3166_GEO_MAP.get(simple_lower(trim_space(token or "")), [])


def lookup_iso3166_geo_harmonics(token: str) -> List[str]:
    """Opt-in HarmonicExpander mapping multilingual ISO 3166 L0-L3 toponyms to canonical (@geo:cc:*, @geo:cont:*) tags.

    The first-seen axis pair wins (geographic entries head the merged table), while
    lookup_closed_set_harmonics emits every axis's companion tags.
    """
    axes = _closed_set_axes(token)
    if not axes:
        return []
    t1, t2 = axes[0]
    return [t1, t2] if t2 and t2 != t1 else [t1]


def lookup_closed_set_harmonics(token: str) -> List[str]:
    """Opt-in HarmonicExpander over the merged closed-set harmonic table (geo/kin/role/cal/bio/obj/venue/units).

    A token owned by several axes emits every axis's companion tags in table order with
    first-seen de-duplication.
    """
    out: List[str] = []
    seen = set()
    for t1, t2 in _closed_set_axes(token):
        for tag in (t1, t2):
            if tag and tag not in seen:
                seen.add(tag)
                out.append(tag)
    return out


def lookup_unit_dimension_harmonic_pair(token: str) -> Tuple[str, str]:
    """Units axis view of the merged closed-set table: the first-seen (@unit:*, @dim:*) pair a token
    owns, or ("", "") when it carries no unit/dimension axis."""
    for t1, t2 in _closed_set_axes(token):
        if t1.startswith("@unit:") and t2.startswith("@dim:"):
            return t1, t2
    return "", ""


def combine_harmonic_expanders(*expanders: Any) -> Optional[Callable[[str], List[str]]]:
    """Merge harmonic expanders into a single expander (in-order, first-seen de-duplication; None entries skipped)."""
    active = [e for e in expanders if callable(e)]
    if not active:
        return None

    def _combined(token: str) -> List[str]:
        out: List[str] = []
        seen = set()
        for e in active:
            for tag in e(token) or []:
                if tag and tag not in seen:
                    seen.add(tag)
                    out.append(tag)
        return out

    return _combined


_INLINE_SYMBOL_UNIT_MAP: Dict[str, str] = {
    "$": "usd",
    "€": "eur",
    "£": "gbp",
    "¥": "cny",
    "￥": "cny",
    "₩": "krw",
    "₹": "inr",
    "₽": "rub",
    "₪": "ils",
    "₺": "try",
    "฿": "thb",
    "₱": "php",
    "₫": "vnd",
    "%": "pct",
    "％": "pct",
    "‰": "ppm",
    "℃": "degc",
}


def extract_inline_symbol_units(sentence: str) -> List[str]:
    if not sentence or len(sentence) < 2:
        return []
    n = len(sentence)
    out: List[str] = []
    seen = set()
    for i, ch in enumerate(sentence):
        prev_digit = i > 0 and is_digit(sentence[i - 1])
        next_digit = i + 1 < n and is_digit(sentence[i + 1])
        if not prev_digit and not next_digit:
            continue
        tok = _INLINE_SYMBOL_UNIT_MAP.get(ch, "")
        if not tok and ch == "°" and i + 1 < n and sentence[i + 1] in ("C", "c", "F", "f"):
            tok = "degc"
        if tok and tok not in seen:
            seen.add(tok)
            out.append(tok)
    return out


_SE_ASIAN_UNIT_WORDS: Tuple[str, ...] = (
    "กิโลกรัม", "กรัม", "กิโลเมตร", "เซนติเมตร", "เมตร", "มิลลิลิตร", "ลิตร",
    "ชั่วโมง", "สัปดาห์", "วินาที", "นาที", "เดือน", "วัน", "ปี", "บาท", "องศา", "เปอร์เซ็นต์", "กิโลวัตต์",
    "ກິໂລກຣາມ", "ກຣາມ", "ກິໂລແມັດ", "ແມັດ", "ລິດ", "ຊົ່ວໂມງ", "ອາທິດ", "ວິນາທີ", "ນາທີ", "ເດືອນ", "ມື້", "ປີ", "ກີບ", "ເປີເຊັນ",
    "គីឡូក្រាម", "ក្រាម", "គីឡូម៉ែត្រ", "ម៉ែត្រ", "លីត្រ", "ម៉ោង", "សប្តាហ៍", "វិនាទី", "នាទី", "ខែ", "ថ្ងៃ", "ឆ្នាំ", "ភាគរយ",
)


def extract_se_asian_unit_words(sentence: str) -> List[str]:
    if not sentence or not trim_space(sentence):
        return []
    out: List[str] = []
    for w in _SE_ASIAN_UNIT_WORDS:
        if w not in sentence:
            continue
        if not any(w in prev for prev in out):
            out.append(w)
    return out


if __name__ == "__main__" and len(sys.argv) >= 4:
    packet, sents = BlindEncoder(sys.argv[1].encode("utf-8")).pack_blind_packet(sys.argv[2], sys.argv[3], 1700000000)
    print(json.dumps({"spec": SPEC_VERSION, "sentences": sents, "packet": packet}, ensure_ascii=False))
