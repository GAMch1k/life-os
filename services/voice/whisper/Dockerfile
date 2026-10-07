FROM alpine:3.22 AS whisper-build
RUN apk add --no-cache build-base cmake git
RUN git clone --depth 1 https://github.com/ggml-org/whisper.cpp.git /src/whisper.cpp
RUN cmake -S /src/whisper.cpp -B /src/whisper.cpp/build -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON -DBUILD_SHARED_LIBS=OFF -DGGML_OPENMP=OFF && cmake --build /src/whisper.cpp/build --target whisper-cli -j2

FROM python:3.13-alpine
RUN apk add --no-cache libstdc++ wget && mkdir /models && chown 65534:65534 /models
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
