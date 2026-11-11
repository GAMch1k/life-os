# The build stage's Alpine is the runtime's (python:3.15-alpine), so
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
# For any CPU of the platform, not the build machine's: ggml's default,
# GGML_NATIVE, compiles for the CPU it builds on, so v0.1.0-preview.5's image,
# built on a release runner with AVX-512, crashed on every server without it.
# GGML_NATIVE=OFF builds for x86-64 with AVX2, FMA, and F16C (ggml's
# defaults then; never AVX-512), and the check below fails a build with
# AVX-512 instructions (zmm registers) in it.
RUN cmake -S /src/whisper.cpp -B /src/whisper.cpp/build -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON -DBUILD_SHARED_LIBS=OFF -DGGML_OPENMP=OFF -DGGML_NATIVE=OFF && cmake --build /src/whisper.cpp/build --target whisper-cli -j2 \
    && ! objdump -d --no-show-raw-insn /src/whisper.cpp/build/bin/whisper-cli | grep -q '%zmm'
# languages.txt (LIFE_OS_WHISPER_LANGUAGE's codes) must be exactly the codes
# of this whisper.cpp's g_lang: the build fails when a new version adds or
# drops one. After the compile, so a change to the list does not rebuild it.
COPY languages.txt /src/languages.txt
RUN sed -n '/^static const std::map<std::string, std::pair<int, std::string>> g_lang = {$/,/^};$/s/^ *{ *"\([a-z]*\)",.*/\1/p' /src/whisper.cpp/src/whisper.cpp | sort >/tmp/whisper-languages \
    && test "$(wc -l </tmp/whisper-languages)" -gt 0 \
    && grep -v '^#' /src/languages.txt | sort | diff -u /tmp/whisper-languages -

FROM python:3.15-alpine@sha256:a0f2e268f2ce8f3709611a90054ec6a8ae31f6251f5896b8648b9bb99835e30a
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
# ffmpeg converts every accepted recording (WebM/Opus, Ogg/Opus, MP4/AAC,
# WAV) to the 16 kHz mono PCM WAV whisper.cpp reads. server.py uses only the
# standard library, so pip (and the copy ensurepip would install again) is
# removed: its vendored packages were most of the image's findings
# (SEC-AUDIT).
# The base image is pinned by digest and moves only when Dependabot bumps it;
# Alpine's security fixes land in between, and the release's Trivy gate fails
# on a HIGH or CRITICAL vulnerability that has a fix: the build upgrades the
# packages (SEC-IMG).
RUN apk upgrade --no-cache \
    && apk add --no-cache libstdc++ wget ffmpeg && mkdir /models && chown 65534:65534 /models \
    && python3 -m pip uninstall --yes --no-cache-dir pip \
    && rm -rf /usr/local/lib/python3.*/ensurepip/_bundled
COPY --from=whisper-build /src/whisper.cpp/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=whisper-build /src/languages.txt /app/languages.txt
COPY server.py /app/server.py
ENV WHISPER_MODEL_PATH=/models/ggml-base.bin \
    WHISPER_LANGUAGE=auto \
    WHISPER_MODEL_URL=https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin \
    WHISPER_THREADS=2 \
    WHISPER_MAX_CONCURRENCY=1 \
    WHISPER_MAX_AUDIO_BYTES=26214400
VOLUME ["/models"]
EXPOSE 8080
USER 65534:65534
ENTRYPOINT ["python3", "/app/server.py"]
