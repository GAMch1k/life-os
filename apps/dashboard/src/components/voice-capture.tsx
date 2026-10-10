"use client";

import { LoaderCircle, Mic, RotateCcw, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { voiceClient } from "@/lib/api";
import { captureMetadata } from "@/lib/time-zone";
import { cn } from "@/lib/utils";
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

/**
 * The Record control. A recording is uploaded to Voice, which transcribes it
 * and creates the Inbox item. `metadata` is added to the upload's
 * `sourceMetadata` (with the browser's timezone), which Voice passes on to
 * that item; `onUploaded` hears when Voice accepted the recording.
 */
export function VoiceCapture({
  metadata,
  onUploaded,
}: {
  metadata?: Record<string, unknown>;
  onUploaded?: (job: VoiceJob) => void;
}) {
  const [state, setState] = useState<CaptureState>("idle");
  const [message, setMessage] = useState(
    "Record a thought; it will be transcribed in the background.",
  );
  const [job, setJob] = useState<VoiceJob>();
  const recorder = useRef<MediaRecorder | undefined>(undefined);
  const chunks = useRef<Blob[]>([]);
  const pendingAudio = useRef<Blob | undefined>(undefined);
  const mounted = useRef(false);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      // Leaving mid-recording (another page, or cancelling a Human Review
      // edit) drops the recording: only Stop sends it. Ending the tracks
      // would otherwise stop the recorder and upload what it has.
      if (recorder.current) recorder.current.onstop = null;
      recorder.current?.stream.getTracks().forEach((track) => track.stop());
    };
  }, []);

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
    let uploaded: VoiceJob | undefined;
    try {
      const body = new FormData();
      body.append("audio", audio, recordingFileName(audio.type));
      body.append("sourceMetadata", JSON.stringify(captureMetadata(metadata)));
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
      uploaded = data;
    } catch {
      const failure = voiceUploadError(status, audio.size);
      if (!failure.retryable) pendingAudio.current = undefined;
      setState("failed");
      setMessage(failure.message);
    }
    // Not after the control is gone, such as an edit cancelled meanwhile.
    if (uploaded && mounted.current) onUploaded?.(uploaded);
  }
  function retry() {
    if (pendingAudio.current) void upload();
    else void start();
  }

  // Presentation: an inline control for the capture card's action row. The
  // status line stays empty until there is something to report; on phones it
  // wraps below the button.
  const quiet = state === "idle" && !job;
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
      {/* Always rendered, so screen readers hear its first message; while
          empty it is sr-only, so on phones it adds no line under the button. */}
      <p
        aria-live="polite"
        className={cn(
          "text-muted-foreground min-w-0 basis-full text-xs sm:flex-1 sm:basis-0",
          quiet && "sr-only",
        )}
      >
        {quiet ? null : message}
      </p>
    </div>
  );
}
