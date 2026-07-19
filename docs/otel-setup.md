# OpenTelemetry Observability

yaah can emit traces to any OpenTelemetry-compatible backend
via OTLP gRPC. This document covers local development setup with
[Jaeger](https://www.jaegertracing.io/) and production options.

## Quick start (Docker Compose)

A `docker-compose.yml` is included in the repo root:

```bash
docker compose up -d jaeger
```

This starts Jaeger v2 all-in-one. The UI has a settings gear icon in
the top-right corner — toggle dark mode there. Ports:

- `16686` — Jaeger UI (http://localhost:16686)
- `4317` — OTLP gRPC (yaah sends traces here)

To run yaah in a container alongside Jaeger:

```bash
docker compose run --rm yaah "your prompt"
```

When yaah runs inside Docker, set the OTel endpoint to `jaeger:4317`
(the Docker service name). When running yaah on the host, use
`localhost:4317`.

## Manual Jaeger setup

```bash
docker run -d --name jaeger \
  -p 16686:16686 -p 4317:4317 \
  cr.jaegertracing.io/jaegertracing/jaeger:2.19.0
```

## Enable in yaah

```yaml
observability:
  otel:
    enabled: true
    endpoint: "localhost:4317"   # or "jaeger:4317" from Docker
    service_name: "yaah"
```

All other fields default as shown. The full reference:

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Set to `true` to activate |
| `endpoint` | `localhost:4317` | OTLP gRPC collector address |
| `service_name` | `yaah` | Display name in the tracing UI |
| `traces` | `true` | Emit trace spans |
| `metrics` | `false` | Emit OTLP metrics (needs a metrics-capable backend) |

## View traces

Open http://localhost:16686 in a browser. Select the `yaah` service from the
dropdown. Each user prompt generates a trace waterfall showing:

| Span | Description |
|---|---|
| `tool.<name>` | One span per tool execution. Duration = tool run time. Errors are marked red. |
| `subagent.<role>` | Sub-agent lifecycle. Spans are nested under the parent turn for waterfall display. |
| `llm.send` | Provider API call. Attributes include `llm.model`, `llm.duration_ms`, and token counts (`llm.prompt_tokens`, `llm.completion_tokens`, `llm.total_tokens`). |

## Traces example

After running `yaah "use a worker sub-agent to list files"`:

```
agent.turn (8.2s)
├── llm.send (4.1s)              # model decides to dispatch a worker
│   model=gpt-4o-mini
│   prompt_tokens=1200 completion_tokens=45 total_tokens=1245
├── subagent.worker (3.9s)       # sub-agent runs
│   ├── tool.ls (12ms)           #   sub-agent lists files
│   ├── tool.glob (8ms)          #   sub-agent finds patterns
│   └── llm.send (3.5s)          #   sub-agent summarises
└── llm.send (0.2s)              # parent summarises result to user
```

## Production setups

### Grafana Tempo (traces) + Prometheus (metrics)

Tempo accepts OTLP directly. Point yaah at the Tempo distributor:

```yaml
observability:
  otel:
    enabled: true
    endpoint: "tempo-distributor:4317"
    service_name: "yaah-prod"
```

### OpenTelemetry Collector

Use the OTel Collector as a routing layer to fan out traces/metrics to
multiple backends:

```yaml
observability:
  otel:
    enabled: true
    endpoint: "otel-collector:4317"
```

### Environment variables

The OTel SDK also respects standard environment variables for advanced
configuration (sampling, batch size, TLS, etc.):

```bash
export OTEL_RESOURCE_ATTRIBUTES="deployment.environment=staging"
export OTEL_TRACES_SAMPLER="parentbased_traceidratio"
export OTEL_TRACES_SAMPLER_ARG="0.1"
```

## Troubleshooting

**"No traces in Jaeger"**: Verify the endpoint is reachable. The Jaeger
container must have port 4317 mapped. Check yaah stderr for OTel errors.

**"Spans appear but no waterfall"**: The default time range may filter them
out — adjust the time picker in the Jaeger UI.

## Disabling

Remove the `observability` block or set `enabled: false`. No traces or
metrics are emitted when disabled. There is zero overhead — the provider
wrapper and executeAndCollect checks are gated on `enabled`.
