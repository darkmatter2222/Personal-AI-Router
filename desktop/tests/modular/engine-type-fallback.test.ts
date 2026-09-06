// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest'

import { EngineTypes, EnabledEngineTypes, EngineDisplayNames } from '@/shared/constants/engines'
import { isEngineType } from '@/shared/utils/engines'
import { engineStatusesToListRows } from '@/ui/utils/get-engines-for-node'
import type { EngineStatusData } from '@/shared/types/engines'

describe('isEngineType', () => {
    it('accepts known engine types', () => {
        expect(isEngineType('ollama')).toBe(true)
        expect(isEngineType('lm-studio')).toBe(true)
    })

    it('rejects unknown engine types', () => {
        expect(isEngineType('vllm')).toBe(false)
        expect(isEngineType('torch-runtime')).toBe(false)
        expect(isEngineType('my-custom-inference')).toBe(false)
        expect(isEngineType('')).toBe(false)
    })

    it('rejects undefined', () => {
        expect(isEngineType(undefined)).toBe(false)
    })
})

describe('EngineTypes const', () => {
    it('is a closed tuple of known engines', () => {
        expect(EngineTypes).toEqual(['ollama', 'lm-studio'])
    })

    it('EnabledEngineTypes is a subset of EngineTypes', () => {
        for (const t of EnabledEngineTypes) {
            expect(EngineTypes).toContain(t)
        }
    })
})

describe('engineDisplayName fallback', () => {
    function displayNameFor(engineType: string): string {
        // Mirrors the logic in get-engines-for-node.ts:39-44
        if (isEngineType(engineType)) {
            return EngineDisplayNames[engineType as 'ollama' | 'lm-studio']
        }
        return engineType
    }

    it('returns the display name for known engines', () => {
        expect(displayNameFor('ollama')).toBe('Ollama')
        expect(displayNameFor('lm-studio')).toBe('LM Studio')
    })

    it('returns the raw string for unknown engines (fallback)', () => {
        expect(displayNameFor('vllm')).toBe('vllm')
        expect(displayNameFor('torch-runtime')).toBe('torch-runtime')
        expect(displayNameFor('my-custom-inference')).toBe('my-custom-inference')
        expect(displayNameFor('engine-test-1')).toBe('engine-test-1')
    })
})

describe('engineStatusesToListRows', () => {
    it('produces rows with display names for known engines', () => {
        const statuses: EngineStatusData[] = [
            {
                engineType: 'ollama',
                nodeId: 'node-1',
                processStatus: 'running',
                enginePort: 11434,
                proxyPort: 54301
            }
        ]
        const rows = engineStatusesToListRows(statuses)
        expect(rows).toHaveLength(1)
        expect(rows[0].displayName).toBe('Ollama')
        expect(rows[0].processStatus).toBe('running')
        expect(rows[0].port).toBe(11434)
    })
})
