# Internal Agent Contracts

- Status: `IMPLEMENTED`
- Authoritative spec: `../spec.md`
- Wire scope: internal typed JSON between bounded model adapters and UseCases; not HTTP

## Common Envelope

```json
{
  "schemaVersion": 1,
  "runId": "server-generated-uuid",
  "sequence": 1,
  "skillId": "recommend_games",
  "skillVersion": "1.0.0"
}
```

`runId`, budgets, allowed delegates/tools, trusted user identity and entitlement
scope are supplied by application state and never trusted from model JSON. Unknown
fields are rejected for every structured model response. String, list and total
encoded-size caps apply before domain use.

## Router Decision

```json
{
  "route": "PLANNING",
  "intent": "game_recommendation",
  "skillId": "recommend_games",
  "skillVersion": "1.0.0",
  "responseMode": "ranked_recommendation"
}
```

Allowed routes are `DIRECT`, `CLARIFY`, `RESEARCH`, and `PLANNING`. Skill identity
must resolve to an embedded enabled definition supporting the route/intent.

## Query Plan

```json
{
  "schemaVersion": 1,
  "units": [
    {
      "id": "q1",
      "text": "co-op RPG games with cross-play",
      "sources": ["lightrag", "catalog", "forum"],
      "lightragMode": "mix",
      "freshness": "stable",
      "filters": {"region": "CN", "platforms": ["pc"]},
      "requiredFacets": ["genre", "multiplayer", "platform"]
    }
  ]
}
```

There are 1-8 unique units; query text is 1-100 Unicode characters. Sources and
filters must be allowed by the Skill and deployment configuration. `lightragMode` is
required exactly when `lightrag` is selected and is restricted to `local`, `global`,
`hybrid` or `mix`; `naive`, `bypass` and unknown values are rejected. The source
means the authenticated official LightRAG `/query/data` adapter, never the legacy
local keyword/vector fallback. No reasoning or instructions field is accepted.

## Research Task And Artifact

```json
{
  "objective": "collect comparable co-op RPG candidates",
  "queryUnitIds": ["q1"],
  "requiredFacets": ["genre", "multiplayer", "platform"],
  "allowedTools": ["search_lightrag", "search_catalog", "search_forum"],
  "remainingBudget": {"modelCalls": 6, "toolCalls": 8, "deadlineMs": 25000}
}
```

The validated result contains `status`, `evidenceIds`, `coveredFacets`,
`missingFacets`, `assumptions`, budget use and `stopReason`. Evidence content lives
in a run-local trusted evidence store; the worker cannot create a valid evidence ID.
The UseCase labels each evidence record with the actual retrieval provider and
LightRAG mode. If LightRAG is unavailable, the worker reports that source as
unavailable and may return only evidence from other Skill-approved sources. Neither
the Agent nor application silently substitutes or relabels the legacy local knowledge
retriever as `lightrag`.

## Planning Task And Artifact

```json
{
  "goal": "rank three games for this player",
  "constraints": {"region": "CN", "platforms": ["pc"], "maxPriceMinor": 30000, "currency": "CNY"},
  "preferenceProjection": {"favoriteGenres": ["rpg"]},
  "ownedEditionIds": ["12"],
  "evidenceIds": ["ev_1", "ev_2"],
  "allowedTools": ["read_catalog", "read_entitlements", "score_constraints"],
  "remainingBudget": {"modelCalls": 4, "toolCalls": 4, "deadlineMs": 15000}
}
```

The result has 1-10 ordered items/steps. Each item contains a stable subject ID, a
short recommendation/action, matched and unmet constraints, assumptions, alternatives
and at least one existing evidence ID for factual claims. The UseCase revalidates
ownership, catalog availability and deterministic constraints after parsing.

## Stop And Degraded Status

Allowed artifact statuses are `complete`, `partial`, `no_result`, `bounded`, and
`unavailable`. Stop reasons are bounded enums such as `complete`, `max_iterations`,
`max_model_calls`, `max_tool_calls`, `max_delegations`, `deadline`, `cancelled`,
`invalid_output`, `dependency_unavailable`, and `no_evidence`. Free-form provider
errors do not cross the adapter boundary.
