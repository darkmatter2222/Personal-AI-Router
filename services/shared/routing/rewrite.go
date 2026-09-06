// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"

	"nvpair-shared/noderec"
)

// RewriteModelAlias rewrites the request body's "model" field to the
// endpoint's physical model name when the requested model matches one of the
// endpoint's declared logical aliases or physical names. If the model is not
// declared for this endpoint, or the body is not a JSON object, the body is
// returned unchanged. This is how one stable client-facing logical name maps
// to different physical model IDs on different runtimes: eligibility already
// matched the request against these same refs (ServesModel), so the rewrite
// and the ownership gate cannot disagree.
func RewriteModelAlias(body []byte, refs []noderec.EngineModelRef, requestedModel string) []byte {
	if len(refs) == 0 || requestedModel == "" {
		return body
	}
	physical, matched := PhysicalForRefs(refs, requestedModel)
	if !matched || physical == requestedModel {
		return body
	}
	var payload map[string]json.RawMessage
	if len(body) == 0 {
		return body
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["model"] = jsonBytes(physical)
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// PhysicalForRefs resolves a requested model name (physical or declared
// logical alias) to the endpoint's physical model name. The first matching
// declaration wins, in ModelRef-first order, which is deterministic.
func PhysicalForRefs(refs []noderec.EngineModelRef, requestedModel string) (string, bool) {
	for i := range refs {
		if requestedModel == refs[i].PhysicalName || containsString(refs[i].Aliases, requestedModel) {
			return refs[i].PhysicalName, true
		}
	}
	return "", false
}

// jsonBytes marshals a value, returning nil on error (a string always
// marshals cleanly; the error path is defensive only).
func jsonBytes(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
