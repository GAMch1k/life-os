"use client";

import { LoaderCircle, Mic, RotateCcw, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { voiceClient } from "@/lib/api";
import { captureMetadata } from "@/lib/time-zone";
import {
  isVoiceJobTerminal,
  type VoiceJob,
  voiceJobMessage,
} from "@/lib/voice-job-state";

type CaptureState = "idle" | "recording" | "uploading" | "failed";

export function VoiceCapture() {
  const [state, setState] = useState<CaptureState>("idle");
  const [message, setMessage] = useState(
    "Record a thought; it will be transcribed in the background.",
  );
  const [job, setJob] = useState<VoiceJob>();
  const recorder = useRef<MediaRecorder | undefined>(undefined);
  const chunks = useRef<Blob[]>([]);
  const pendingAudio = useRef<Blob | undefined>(undefined);

  useEffect(
    () => () =>
      recorder.current?.stream.getTracks().forEach((track) => track.stop()),
    [],
  );

  useEffect(() => {
    if (!job || isVoiceJobTerminal(job.status)) return;
    const timer = window.setInterval(() => {
      void voiceClient
        .GET("/v1/audio-jobs/{jobId}", { params: { path: { jobId: job.id } } })
        .then(({ data }) => {
          if (!data) return;
          setJob(data);
          setMessage(voiceJobMessage(data));
        })
        .catch(() => setMessage("Checking transcription status again soon…"));
    }, 2000);
    return () => window.clearInterval(timer);
  }, [job]);

  async function start() {
    if (!navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) {
      setState("failed");
      setMessage("This browser does not support microphone recording.");
      return;
    }
    try {
      setJob(undefined);
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      chunks.current = [];
      const next = new MediaRecorder(stream);
      next.ondataavailable = (event) => {
        if (event.data.size) chunks.current.push(event.data);
      };
      next.onstop = () => {
        stream.getTracks().forEach((track) => track.stop());
        pendingAudio.current = new Blob(chunks.current, {
          type: next.mimeType || "audio/webm",
        });
        void upload();
      };
      recorder.current = next;
      next.start();
      setState("recording");
      setMessage("Recording… tap Stop when you are done.");
    } catch {
      setState("failed");
      setMessage(
        "Microphone access was not granted. You can try again when ready.",
      );
    }
  }
  function stop() {
    recorder.current?.stop();
    setState("uploading");
    setMessage("Capture stopped; sending it to the transcription queue…");
  }
  async function upload() {
    const audio = pendingAudio.current;
    if (!audio) return;
    setState("uploading");
    try {
      const body = new FormData();
      body.append("audio", audio, "voice-capture.webm");
      body.append("sourceMetadata", JSON.stringify(captureMetadata()));
      body.append("source", "web");
      const { data, error } = await voiceClient.POST("/v1/audio-jobs", {
        body: body as never,
      });
      if (!data || error) throw new Error("upload failed");
      setJob(data);
      setState("idle");
      setMessage(voiceJobMessage(data));
    } catch {
      setState("failed");
      setMessage("Could not upload this recording. Try sending it again.");
    }
  }
  function retry() {
    if (pendingAudio.current) void upload();
    else void start();
  }

  // Presentation: an inline control for the capture card's action row. The
  // status line stays empty until there is something to report; on phones it
  // wraps below the button.
  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
      {state === "recording" ? (
        <Button onClick={stop} size="lg" variant="destructive">
          <Square /> Stop
        </Button>
      ) : state === "uploading" ? (
        <Button disabled size="lg" variant="outline">
          <LoaderCircle className="animate-spin" /> Sending
        </Button>
      ) : state === "failed" ||
        (job &&
          isVoiceJobTerminal(job.status) &&
          job.status !== "succeeded") ? (
        <Button onClick={retry} size="lg" variant="outline">
          <RotateCcw /> Retry
        </Button>
      ) : (
        <Button onClick={start} size="lg" variant="outline">
          <Mic /> Record
        </Button>
      )}
      <p
        aria-live="polite"
        className="text-muted-foreground min-w-0 basis-full text-xs sm:flex-1 sm:basis-0"
      >
        {state === "idle" && !job ? null : message}
      </p>
    </div>
  );
}
