# Phase 6 verification

The web voice path is deliberately asynchronous: a browser recording posts to the Voice API, receives `202`, and later polls its job while the worker transcribes through the private Whisper container. A completed job creates a Voice Inbox item with a deterministic dedupe key; the Phase 5 processor is then responsible for constrained MCP action routing.

Local verification exercised the multilingual Whisper worker with English (`jfk.wav`), Russian, and German speech samples. The live Voice smoke verified upload acceptance, a Whisper failure recorded as `retryable`, worker retry, a succeeded job with transcript, status polling through `/api/voice`, and exactly one corresponding Inbox item. The processor routing corpus additionally proves a Voice transcript can create a Task through MCP without bypassing its audit/resolution rules.

Useful checks:

```sh
make -C services/voice check
cd services/mcp && go test ./internal/processor
npm --prefix apps/dashboard run check
```
