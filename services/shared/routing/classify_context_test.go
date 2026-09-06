// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "testing"

func TestClassify_DefaultOutputReserve(t *testing.T) {
	// Unspecified output must reserve the default rather than zero.
	r := Classify([]byte(`{"model":"m","prompt":"hello there friend"}`))
	if r.OutputTokensReq != 0 {
		t.Fatalf("unspecified output should parse as 0, got %d", r.OutputTokensReq)
	}
	if r.RequiredContext < r.InputTokensEst+DefaultOutputReserveTokens {
		t.Fatalf("unspecified output must reserve >= %d: RequiredContext=%d input=%d",
			DefaultOutputReserveTokens, r.RequiredContext, r.InputTokensEst)
	}
}

func TestClassify_ExplicitOutputOverridesReserve(t *testing.T) {
	// A small explicit output is used as-is, not bumped to the default reserve.
	r := Classify([]byte(`{"model":"m","prompt":"hi","max_tokens":100}`))
	if r.OutputTokensReq != 100 {
		t.Fatalf("output = %d, want 100", r.OutputTokensReq)
	}
	if r.RequiredContext != r.InputTokensEst+100 {
		t.Fatalf("RequiredContext = %d, want input(%d)+100 with no default reserve, no images",
			r.RequiredContext, r.InputTokensEst)
	}
}

func TestClassify_ImageReserve(t *testing.T) {
	one := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"x"}}]}]}`))
	if one.ImageCount != 1 || !one.HasImages {
		t.Fatalf("one image: count=%d hasImages=%v", one.ImageCount, one.HasImages)
	}
	if one.RequiredContext < DefaultImageTokenReservePerImage {
		t.Fatalf("one image should reserve >= %d, RequiredContext=%d", DefaultImageTokenReservePerImage, one.RequiredContext)
	}
	// Hold the non-image text identical to the one-image case so the delta is
	// purely the extra image's reserve.
	two := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"a"}},{"type":"image_url","image_url":{"url":"b"}}]}]}`))
	if two.ImageCount != 2 {
		t.Fatalf("two images: count=%d", two.ImageCount)
	}
	if two.RequiredContext < one.RequiredContext+DefaultImageTokenReservePerImage {
		t.Fatalf("two images should reserve more than one: two=%d one=%d", two.RequiredContext, one.RequiredContext)
	}
	// Ollama top-level and per-message images also count.
	if ol := Classify([]byte(`{"model":"m","prompt":"hi","images":["a","b","c"]}`)); ol.ImageCount != 3 {
		t.Fatalf("ollama top-level images = %d, want 3", ol.ImageCount)
	}
	if msg := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":"hi","images":["a"]}]}`)); msg.ImageCount != 1 {
		t.Fatalf("ollama message images = %d, want 1", msg.ImageCount)
	}
}

func TestClassify_OverflowSaturates(t *testing.T) {
	// A huge explicit output saturates instead of wrapping to a small/negative.
	r := Classify([]byte(`{"model":"m","prompt":"hi","max_tokens":2000000000}`))
	if r.RequiredContext != maxTokenEstimate {
		t.Fatalf("huge output should saturate RequiredContext at %d, got %d", maxTokenEstimate, r.RequiredContext)
	}
	if r.RequiredContext < 0 {
		t.Fatal("RequiredContext must never be negative")
	}
}

func TestSaturatingArithmetic(t *testing.T) {
	if satAdd(-5, -5) != 0 {
		t.Fatal("satAdd of negatives -> 0")
	}
	if satAdd(maxTokenEstimate, maxTokenEstimate) != maxTokenEstimate {
		t.Fatal("satAdd caps at maxTokenEstimate")
	}
	if satAdd(3, 4) != 7 {
		t.Fatal("satAdd normal")
	}
	if satMul(0, 5) != 0 || satMul(5, 0) != 0 {
		t.Fatal("satMul with zero -> 0")
	}
	if satMul(maxTokenEstimate, 2) != maxTokenEstimate {
		t.Fatal("satMul caps at maxTokenEstimate")
	}
	if satMul(3, 4) != 12 {
		t.Fatal("satMul normal")
	}
	if v := estimateTokensFromChars(maxTokenEstimate * 4); v <= 0 || v > maxTokenEstimate {
		t.Fatalf("estimateTokensFromChars should saturate within (0, %d], got %d", maxTokenEstimate, v)
	}
}
