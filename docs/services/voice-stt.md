# Voice and STT (planned)

Voice/STT is not implemented yet. Phase 6 will accept a browser recording, create an asynchronous job, run CPU-friendly self-hosted `whisper.cpp` with RU/DE/EN model configuration, persist transcription state, then capture the text into Inbox. The downstream action path remains Inbox → MCP → core service API.

`/api/voice/` is reserved at the gateway and unavailable until that contract exists. Android and Wear OS capture clients are deliberately out of scope for this repository.
