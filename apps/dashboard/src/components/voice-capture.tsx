"use client";

import { LoaderCircle, Mic, RotateCcw, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { voiceClient } from "@/lib/api";
import {
  isVoiceJobTerminal,
  pickRecordingType,
  recordingFileName,
  type VoiceJob,
  voiceJobMessage,
  voiceRecordingProblem,
  voiceUploadError,
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
      // Ask for a format Voice accepts instead of the browser default.
      const mimeType = pickRecordingType((type) =>
        MediaRecorder.isTypeSupported(type),
      );
      const next = new MediaRecorder(
        stream,
        mimeType ? { mimeType } : undefined,
      );
      next.ondataavailable = (event) => {
        if (event.data.size) chunks.current.push(event.data);
      };
      next.onstop = () => {
        stream.getTracks().forEach((track) => track.stop());
        pendingAudio.current = new Blob(chunks.current, {
          type: next.mimeType || mimeType || "",
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
    // An unusable recording cannot be retried; Retry records a new one.
    const problem = voiceRecordingProblem(audio.size);
    if (problem) {
      pendingAudio.current = undefined;
      setState("failed");
      setMessage(problem);
      return;
    }
    setState("uploading");
    let status: number | undefined;
    try {
      const body = new FormData();
      body.append("audio", audio, recordingFileName(audio.type));
      body.append("source", "web");
      const { data, error, response } = await voiceClient.POST(
        "/v1/audio-jobs",
        { body: body as never },
      );
      status = response.status;
      if (!data || error) throw new Error("upload failed");
      setJob(data);
      setState("idle");
      setMessage(voiceJobMessage(data));
    } catch {
      const failure = voiceUploadError(status, audio.size);
      if (!failure.retryable) pendingAudio.current = undefined;
      setState("failed");
      setMessage(failure.message);
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
      ) : state === "failed" ||
        (job &&
          isVoiceJobTerminal(job.status) &&
          job.status !== "succeeded") ? (
        <Button onClick={retry} size="sm" variant="outline">
          <RotateCcw /> Retry
        </Button>
      ) : (
        <Button onClick={start} size="sm">
          <Mic /> {job ? "Record again" : "Record"}
        </Button>
      )}
    </div>
  );
}
