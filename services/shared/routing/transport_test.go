// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"net/http"
	"testing"
	"time"
)

func TestProfileFor(t *testing.T) {
	def := 5 * time.Second
	p := ProfileFor(Timeouts{ConnectMS: 100, ResponseHeaderMS: 30000}, def, def)
	if p.Connect != 100*time.Millisecond {
		t.Fatalf("connect = %v, want 100ms", p.Connect)
	}
	if p.ResponseHeader != 30*time.Second {
		t.Fatalf("responseHeader = %v, want 30s", p.ResponseHeader)
	}
	z := ProfileFor(Timeouts{}, def, def)
	if z.Connect != def || z.ResponseHeader != def {
		t.Fatalf("unset fields should use defaults: %+v", z)
	}
}

// TestDialerForConsumesConnectTimeout proves ConnectMS is actually threaded onto
// the dialer, and that two endpoints get independent dial timeouts.
func TestDialerForConsumesConnectTimeout(t *testing.T) {
	if d := dialerFor(TransportProfile{Connect: 100 * time.Millisecond}); d.Timeout != 100*time.Millisecond {
		t.Fatalf("dialer timeout = %v, want 100ms", d.Timeout)
	}
	if d := dialerFor(TransportProfile{Connect: 2 * time.Second}); d.Timeout != 2*time.Second {
		t.Fatalf("dialer timeout = %v, want 2s", d.Timeout)
	}
}

func TestTransportFactory_PerProfileIsolationAndCaching(t *testing.T) {
	f := NewTransportFactory(nil)
	pA := TransportProfile{Connect: 100 * time.Millisecond, ResponseHeader: 1 * time.Second}
	pB := TransportProfile{Connect: 2 * time.Second, ResponseHeader: 10 * time.Second}
	tA := f.For(pA)
	tB := f.For(pB)
	if tA == tB {
		t.Fatal("distinct profiles must yield distinct transports (per-endpoint isolation)")
	}
	if tA2 := f.For(pA); tA2 != tA {
		t.Fatal("same profile must be cached, not rebuilt per request")
	}
	if f.Len() != 2 {
		t.Fatalf("factory built %d transports, want 2", f.Len())
	}
	if tA.ResponseHeaderTimeout != 1*time.Second || tB.ResponseHeaderTimeout != 10*time.Second {
		t.Fatalf("per-profile header timeouts wrong: A=%v B=%v", tA.ResponseHeaderTimeout, tB.ResponseHeaderTimeout)
	}
	if tA.DialContext == nil || tB.DialContext == nil {
		t.Fatal("dial context must be set on each transport")
	}
}

func TestTransportFactory_ClonePreservesTemplate(t *testing.T) {
	tmpl := &http.Transport{MaxIdleConnsPerHost: 7}
	f := NewTransportFactory(tmpl)
	got := f.For(TransportProfile{Connect: time.Second})
	if got.MaxIdleConnsPerHost != 7 {
		t.Fatalf("clone dropped template setting: %d", got.MaxIdleConnsPerHost)
	}
	if got == tmpl {
		t.Fatal("factory must clone, not mutate, the template")
	}
}
