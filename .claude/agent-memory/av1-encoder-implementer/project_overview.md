---
name: Project Overview
description: Pure Go AV1 codec project overview — module name, Go version, no external deps, parallel agent architecture
type: project
---

av1go is a pure Go AV1 encoder/decoder project (module: av1go, Go 1.26.1, zero external dependencies).

**Why:** Educational/research AV1 implementation in pure Go — no CGo, no libaom/SVT-AV1 bindings.

**How to apply:** All codec internals must be implemented from scratch in Go. Use AV1 spec section references for all syntax elements. A decoder agent works in parallel and creates reader/parser counterparts — always check for existing types in obu/types.go before defining new ones. Shared types (FrameType, SequenceHeader, ColorConfig, etc.) live in obu/types.go to be used by both encoder and decoder.
