// mention.ts — pure helpers for @mention autocomplete in the comment composer
// (task-comment-composer-fix, FEAT-C). Kept pure (no React, no client) so they
// are unit-testable in isolation; the component wires them into textarea
// onChange/onKeyDown.
//
// detectMention inspects the text BEFORE the caret and decides whether the
// caret is inside an active @<query> token. applyMention replaces that token
// with @<username> (plus a trailing space) and returns the new caret position.

export interface MentionDetection {
    open: boolean;
    // start is the character index of the '@' (when open). Undefined when closed.
    start?: number;
    // query is the text typed after '@' (may be empty right after pressing '@').
    query: string;
}

// detectMention returns whether the caret sits in an @<query> token. The token
// is recognized only when the '@' is preceded by either the start of the text
// or a whitespace character — so an '@' inside an email (a@b.com) or an
// already-mention-shaped word does NOT trigger. The query is the run of
// username-legal characters (\w) after the '@'.
export function detectMention(text: string, caret: number): MentionDetection {
    const before = text.slice(0, caret);
    const m = before.match(/(^|\s)@(\w*)$/);
    if (!m) {
        return {open: false, query: ''};
    }
    // start = index of the '@' = (end of before) - len("@query").
    const start = before.length - m[2].length - 1;
    return {open: true, start, query: m[2]};
}

export interface MentionInsert {
    text: string;
    caret: number;
}

// applyMention replaces the @<query> token (described by detection.start +
// detection.query) with "@<username> " and returns the resulting full text plus
// the caret position immediately after the trailing space. The leading
// whitespace captured by detectMention (the char before '@') is PRESERVED —
// only the '@query' run is substituted.
export function applyMention(
    text: string,
    caret: number,
    detection: MentionDetection,
    username: string,
): MentionInsert {
    if (!detection.open || detection.start === undefined) {
        return {text, caret};
    }
    const atIdx = detection.start;
    const tokenEnd = atIdx + 1 + detection.query.length; // past '@query'
    const mention = `@${username} `;
    const next = text.slice(0, atIdx) + mention + text.slice(tokenEnd);
    const nextCaret = atIdx + mention.length;
    return {text: next, caret: nextCaret};
}
