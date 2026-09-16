// Shape of the in-app legal documents. Deliberately plain data rather than
// markdown: rendering markdown would mean shipping a parser for two static
// screens, and structured sections let the legal screen build a table of
// contents without re-parsing prose.

export interface LegalSection {
  /** Numbered heading, e.g. "4. How we use your information". */
  heading: string;
  /** Paragraphs, rendered in order, before any bullets. */
  body?: string[];
  /** Bulleted items, rendered after `body`. */
  bullets?: string[];
  /** Paragraphs rendered after the bullets — for a closing note that only
   *  makes sense once the reader has seen the list. */
  footnote?: string[];
}

export interface LegalDocument {
  title: string;
  /** Bumped whenever the substance changes; shown to the user. */
  version: string;
  /** Human-readable date, e.g. "24 July 2026". */
  effectiveDate: string;
  /** Canonical public URL — the same text is published on the website. */
  url: string;
  /** Lead paragraphs shown before the numbered sections. */
  intro: string[];
  sections: LegalSection[];
}
