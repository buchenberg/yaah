# OpenTelemetry Observability

yaah can emit traces to any OpenTelemetry-compatible backend
via OTLP gRPC. This document covers local development setup with
[Jaeger](https://www.jaegertracing.io/) (traces) and
production options.

## Quick start (Jaeger all-in-one)

Start Jaeger with a single Docker command:

```bash
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  -p 4318:4318 \
  jaegertracing/all-in-one:latest
```

Ports:
- `16686` — Jaeger web UI
- `4317` — OTLP gRPC (yaah sends traces here)
- `4318` — OTLP HTTP (alternative; not used by yaah)

## Enable in yaah

Add to `~/.yaah/config.yaml`:

```yaml
observability:
  otel:
    enabled: true
    endpoint: "localhost:4317"
    service_name: "yaah"
    traces: true
    metrics: false
```

All fields are optional with these defaults:

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

**"No traces in Jaeger"**: Verify the endpoint is reachable. Jaeger's
all-in-one container must have port 4317 mapped. Check yaah stderr for
OTel errors (logged at startup).

**"Spans appear but no waterfall"**: Jaeger's default view shows the last
hour. Refresh the time range.

**"Error: resource exhausted"**: The gRPC client may need a larger
message size. This is rare — contact the project if you encounter this.

## Disabling

Remove the `observability` block or set `enabled: false`. No traces or
metrics are emitted when disabled. There is zero overhead — the provider
wrapper and executeAndCollect checks are gated on `enabled`.
