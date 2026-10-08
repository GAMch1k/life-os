"use client";

import { LoaderCircle, Mic, RotateCcw, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";

type CaptureState = "idle" | "recording" | "uploading" | "accepted" | "failed";

export function VoiceCapture() {
  const [state, setState] = useState<CaptureState>("idle");
  const [message, setMessage] = useState(
    "Record a thought; it will be transcribed in the background.",
  );
  const recorder = useRef<MediaRecorder | undefined>(undefined);
  const chunks = useRef<Blob[]>([]);
  const pendingAudio = useRef<Blob | undefined>(undefined);

  useEffect(
    () => () =>
      recorder.current?.stream.getTracks().forEach((track) => track.stop()),
    [],
  );

  async function start() {
    if (!navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) {
      setState("failed");
      setMessage("This browser does not support microphone recording.");
      return;
    }
    try {
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
      body.append("source", "web");
      const response = await fetch("/api/voice/v1/audio-jobs", {
        method: "POST",
        body,
      });
      if (!response.ok) throw new Error("upload failed");
      setState("accepted");
      setMessage(
        "Voice capture accepted. It will appear in Inbox after transcription.",
      );
    } catch {
      setState("failed");
      setMessage("Could not upload this recording. Try sending it again.");
    }
  }
  function retry() {
    if (pendingAudio.current) void upload();
    else void start();
  }

  return (
    <div className="flex items-center gap-3">
      <span className="bg-background flex size-10 items-center justify-center rounded-xl border shadow-sm">
        <Mic aria-hidden="true" className="size-4" />
      </span>
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">Voice capture</p>
        <p aria-live="polite" className="text-muted-foreground text-xs">
          {message}
        </p>
      </div>
      {state === "recording" ? (
        <Button onClick={stop} size="sm" variant="destructive">
          <Square /> Stop
        </Button>
      ) : state === "uploading" ? (
        <Button disabled size="sm">
          <LoaderCircle className="animate-spin" /> Sending
        </Button>
      ) : state === "failed" ? (
        <Button onClick={retry} size="sm" variant="outline">
          <RotateCcw /> Retry
        </Button>
      ) : (
        <Button onClick={start} size="sm">
          <Mic /> {state === "accepted" ? "Record again" : "Record"}
        </Button>
      )}
    </div>
  );
}
