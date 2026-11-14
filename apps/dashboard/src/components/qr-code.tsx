import { encode } from "uqr";

/**
 * A QR code of `text` as an SVG, dark on white in either theme so that a
 * phone's camera reads it. Drawn in the browser: what it encodes (such as a
 * TOTP secret) is never sent anywhere to be rendered.
 */
export function QrCode({
  className,
  label,
  text,
}: {
  className?: string;
  label: string;
  text: string;
}) {
  const { data, size } = encode(text, { ecc: "M", border: 2 });
  let path = "";
  data.forEach((row, y) =>
    row.forEach((dark, x) => {
      if (dark) path += `M${x} ${y}h1v1h-1z`;
    }),
  );
  return (
    <svg
      aria-label={label}
      className={className}
      role="img"
      shapeRendering="crispEdges"
      viewBox={`0 0 ${size} ${size}`}
    >
      <rect fill="#fff" height={size} width={size} />
      <path d={path} fill="#000" />
    </svg>
  );
}
