# The build stage's Alpine is the runtime's (python:3.13-alpine), so
# whisper-cli links against the libstdc++ it runs with.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS whisper-build
RUN apk add --no-cache build-base cmake git
# A fixed whisper.cpp release rather than whatever was newest at build time:
# v1.9.5 is the version the image had been built from (October 2026). The
# commit check fails the build should the tag ever point elsewhere. Update
# both together, deliberately.
ARG WHISPER_CPP_VERSION=v1.9.5
ARG WHISPER_CPP_COMMIT=d1be6fde11ac6e0407606b4e42fe72d34add8037
RUN git clone --depth 1 --branch "$WHISPER_CPP_VERSION" https://github.com/ggml-org/whisper.cpp.git /src/whisper.cpp \
    && test "$(git -C /src/whisper.cpp rev-parse HEAD)" = "$WHISPER_CPP_COMMIT"
RUN cmake -S /src/whisper.cpp -B /src/whisper.cpp/build -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON -DBUILD_SHARED_LIBS=OFF -DGGML_OPENMP=OFF && cmake --build /src/whisper.cpp/build --target whisper-cli -j2

FROM python:3.13-alpine@sha256:1e1f6334529d1fb26a2ef619d7cffb30a7c7d7cc1cc7338ebee283a58d1aeaca
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
# ffmpeg converts every accepted recording (WebM/Opus, Ogg/Opus, MP4/AAC,
# WAV) to the 16 kHz mono PCM WAV whisper.cpp reads. server.py uses only the
# standard library, so pip (and the copy ensurepip would install again) is
# removed: its vendored packages were most of the image's findings
# (SEC-AUDIT).
RUN apk add --no-cache libstdc++ wget ffmpeg && mkdir /models && chown 65534:65534 /models \
    && python3 -m pip uninstall --yes --no-cache-dir pip \
    && rm -rf /usr/local/lib/python3.*/ensurepip/_bundled
COPY --from=whisper-build /src/whisper.cpp/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY server.py /app/server.py
ENV WHISPER_MODEL_PATH=/models/ggml-base.bin \
    WHISPER_MODEL_URL=https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin \
    WHISPER_THREADS=2 \
    WHISPER_MAX_CONCURRENCY=1 \
    WHISPER_MAX_AUDIO_BYTES=26214400
VOLUME ["/models"]
EXPOSE 8080
USER 65534:65534
ENTRYPOINT ["python3", "/app/server.py"]
