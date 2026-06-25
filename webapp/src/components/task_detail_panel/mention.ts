// mention.ts — pure helpers for @mention autocomplete in the comment composer
// (task-comment-composer-fix, FEAT-C). Kept pure (no React, no client) so they
// are unit-testable in isolation; the component wires them into textarea
// onChange/onKeyDown.
//
// detectMention finds the @<word> token that SURROUNDS the caret (the @word may
// extend past the caret). applyMention replaces that whole token with
// @<username> (plus a trailing space) and returns the new caret position.

export interface MentionDetection {
    open: boolean;

    // start is the character index of the '@' (when open). Undefined when closed.
    start?: number;

    // tokenEnd is the index just past the '@word' token (when open).
    tokenEnd?: number;

    // query is the ENTIRE word after '@' (including any suffix past the caret).
    query: string;
}

const MENTION_TOKEN = /(^|\s)@(\w*)/g;

// detectMention returns whether the caret sits inside (or immediately after) an
// @<word> token. The token's '@' must be preceded by start-of-text or a
// whitespace char, so an '@' inside an email (a@b.com) does NOT trigger. The
// query covers the WHOLE word following the '@' — including characters past
// the caret — so applyMention replaces the complete token, not a truncated
// prefix.
export function detectMention(text: string, caret: number): MentionDetection {
    if (caret < 0 || caret > text.length) {
        return {open: false, query: ''};
    }
    let m: RegExpExecArray | null;
    MENTION_TOKEN.lastIndex = 0;
    while ((m = MENTION_TOKEN.exec(text)) !== null) {
        // m.index is the start of the leading (^|\s) group; the '@' sits one
        // char after it (or at m.index when the group matched '^').
        const atIdx = m.index + m[1].length;
        const word = m[2];
        const tokenEnd = atIdx + 1 + word.length;

        // Caret must be inside the token: after the '@' and at/before tokenEnd.
        if (caret > atIdx && caret <= tokenEnd) {
            return {open: true, start: atIdx, tokenEnd, query: word};
        }
        if (m.index === text.length) {
            break; // zero-length progress guard
        }
    }
    return {open: false, query: ''};
}

export interface MentionInsert {
    text: string;
    caret: number;
}

// applyMention replaces the @<word> token (described by detection.start /
// detection.tokenEnd) with "@<username> " and returns the resulting full text
// plus the caret position immediately after the trailing space. The leading
// whitespace before the '@' is PRESERVED — only the '@word' run is substituted.
export function applyMention(
    text: string,
    caret: number,
    detection: MentionDetection,
    username: string,
): MentionInsert {
    if (!detection.open || detection.start === undefined || detection.tokenEnd === undefined) {
        return {text, caret};
    }
    const atIdx = detection.start;
    const tokenEnd = detection.tokenEnd;
    const mention = `@${username} `;
    const next = text.slice(0, atIdx) + mention + text.slice(tokenEnd);
    const nextCaret = atIdx + mention.length;
    return {text: next, caret: nextCaret};
}
