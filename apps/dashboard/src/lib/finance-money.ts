// Money as the Finance API has it: an integer amount in the currency's minor
// units (cents), negative for money out, with a three-letter ISO currency.
// Amounts are only ever converted to decimals for display; what the owner
// types is parsed straight to minor units with string arithmetic, so no
// floating-point rounding reaches the API.

export type Money = { amount: number; currency: string };

const digitsCache = new Map<string, number>();

/** How many minor-unit digits a currency has: 2 for EUR, 0 for JPY. */
export function minorDigits(currency: string) {
  let digits = digitsCache.get(currency);
  if (digits === undefined) {
    try {
      digits = new Intl.NumberFormat("en", {
        style: "currency",
        currency,
      }).resolvedOptions().maximumFractionDigits;
    } catch {
      // Not a currency Intl knows: cents, like most.
      digits = 2;
    }
    digitsCache.set(currency, digits ?? 2);
  }
  return digits ?? 2;
}

const formats = new Map<string, Intl.NumberFormat>();

function currencyFormat(locale: string, currency: string, signed: boolean) {
  const id = `${locale}|${currency}|${signed}`;
  let format = formats.get(id);
  if (!format) {
    const options: Intl.NumberFormatOptions = {
      style: "currency",
      currency,
      signDisplay: signed ? "exceptZero" : "auto",
    };
    try {
      format = new Intl.NumberFormat(locale, options);
    } catch {
      format = new Intl.NumberFormat("en-GB", options);
    }
    formats.set(id, format);
  }
  return format;
}

/**
 * An amount in `locale` (the browser's): "-€12.50" in en-GB, "-12,50 €" in
 * de-DE. `signed` also marks money in with "+", for transaction lists.
 */
export function formatMoney(
  money: Money,
  locale: string,
  { signed = false }: { signed?: boolean } = {},
) {
  const scale = 10 ** minorDigits(money.currency);
  return currencyFormat(locale, money.currency, signed).format(
    money.amount / scale,
  );
}

/**
 * An amount's size as an editable number in `locale`, without the sign,
 * currency, or grouping: 1234.5 EUR is "1234.50" in en-GB, "1234,50" in
 * de-DE. parseAmount reads it back.
 */
export function amountInput(minor: number, currency: string, locale: string) {
  const digits = minorDigits(currency);
  const options = {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
    useGrouping: false,
  };
  let format: Intl.NumberFormat;
  try {
    format = new Intl.NumberFormat(locale, options);
  } catch {
    format = new Intl.NumberFormat("en-GB", options);
  }
  return format.format(Math.abs(minor) / 10 ** digits);
}

/**
 * Typed text as minor units of the currency, or undefined when it is not an
 * amount. Both decimal marks work, as people write them: "12.50", "12,50",
 * "1,234.56", "1.234,56", "1 234,56". A lone mark followed by exactly three
 * digits groups thousands ("1.234" is 1234); more decimals than the currency
 * has are refused rather than rounded. A leading "-" makes it negative; a
 * currency sign or code around the number is ignored.
 */
export function parseAmount(text: string, currency: string) {
  const digits = minorDigits(currency);
  const compact = text.replace(/[\s  '’]/g, "");
  // A minus before the first digit, as in "-12,50" or "-€12.50".
  const negative = /^[^\d]*[-−]/.test(compact);
  const value = compact.replace(/^[^\d.,]+|[^\d.,]+$/g, "");
  if (!/^[\d.,]+$/.test(value) || !/\d/.test(value)) return undefined;

  const lastComma = value.lastIndexOf(",");
  const lastDot = value.lastIndexOf(".");
  let decimalMark: "," | "." | undefined;
  if (lastComma >= 0 && lastDot >= 0) {
    decimalMark = lastComma > lastDot ? "," : ".";
  } else if (lastComma >= 0 || lastDot >= 0) {
    const mark = lastComma >= 0 ? "," : ".";
    const parts = value.split(mark);
    // One mark with one to (a currency's) decimals after it is a decimal
    // point; three digits after each mark group thousands.
    if (parts.length === 2 && parts[1].length !== 3) decimalMark = mark;
    else if (parts.length === 2 && parts[0] === "") decimalMark = mark;
  }

  const [whole, fraction = "", ...rest] = decimalMark
    ? value.split(decimalMark)
    : [value];
  if (rest.length) return undefined;
  const groupMark = decimalMark === "," ? "." : ",";
  const groups = whole.split(decimalMark ? groupMark : /[.,]/);
  if (
    groups.length > 1 &&
    (groups[0].length === 0 ||
      groups[0].length > 3 ||
      groups.slice(1).some((group) => group.length !== 3))
  ) {
    return undefined;
  }
  const integer = groups.join("");
  if (!/^\d*$/.test(integer) || !/^\d*$/.test(fraction)) return undefined;
  if (!integer && !fraction) return undefined;
  if (fraction.length > digits) return undefined;

  const minor = Number(`${integer || "0"}${fraction.padEnd(digits, "0")}`);
  if (!Number.isSafeInteger(minor)) return undefined;
  return negative && minor !== 0 ? -minor : minor;
}

/** A transaction amount's direction: money in, or money out. */
export function directionOf(amount: number): "in" | "out" {
  return amount < 0 ? "out" : "in";
}

/** An amount's size with the direction's sign: money out is negative. */
export function signed(size: number, direction: "in" | "out") {
  const magnitude = Math.abs(size);
  return direction === "out" ? -magnitude : magnitude;
}
