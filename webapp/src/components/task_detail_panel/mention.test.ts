// Unit tests for the pure @mention helpers (task-comment-composer-fix, FEAT-C).
// detectMention/applyMention are framework-free; these pin the trigger rules
// (open after whitespace/start-of-text, NOT inside email) and the insert math.

import {
    applyMention,
    detectMention,
} from 'components/task_detail_panel/mention';

describe('detectMention — @ trigger detection (FEAT-C task 3.1)', () => {
    test('opens after a leading space + @ + letters', () => {
        // text: "hi @al|"  (| = caret at end)
        const d = detectMention('hi @al', 6);
        expect(d.open).toBe(true);
        expect(d.query).toBe('al');
        expect(d.start).toBe(3); // index of '@'
    });

    test('opens at start-of-text with @ + letters', () => {
        const d = detectMention('@carol', 6);
        expect(d.open).toBe(true);
        expect(d.query).toBe('carol');
        expect(d.start).toBe(0);
    });

    test('opens with an empty query right after pressing @', () => {
        const d = detectMention('hi @', 4);
        expect(d.open).toBe(true);
        expect(d.query).toBe('');
        expect(d.start).toBe(3);
    });

    test('does NOT open inside an email (no space before @)', () => {
        const d = detectMention('naicoi@example.com', 18);
        expect(d.open).toBe(false);
    });

    test('does NOT open when there is plain text with no @', () => {
        const d = detectMention('hello world', 11);
        expect(d.open).toBe(false);
    });

    test('closes once the caret moves past the token (space after query)', () => {
        // caret after the trailing space: "@carol |"
        const d = detectMention('@carol ', 7);
        expect(d.open).toBe(false);
    });

    test('query stops at the first non-word char', () => {
        // typing "@al!" — '!' breaks the token; caret at end
        const d = detectMention('hi @al!', 7);
        expect(d.open).toBe(false);
    });
});

describe('applyMention — insert @username and reposition caret (FEAT-C)', () => {
    test('replaces @query with @username + trailing space', () => {
        const text = 'hi @al there';
        const caret = 6; // after "@al"
        const d = detectMention(text, caret);
        const ins = applyMention(text, caret, d, 'alice');
        expect(ins.text).toBe('hi @alice  there');
        expect(ins.caret).toBe(3 + '@alice '.length); // after the trailing space
    });

    test('preserves the leading whitespace before @', () => {
        const text = 'x @car';
        const d = detectMention(text, 6);
        const ins = applyMention(text, 6, d, 'carol');
        expect(ins.text).toBe('x @carol ');
    });

    test('handles token at start of text', () => {
        const text = '@ca';
        const d = detectMention(text, 3);
        const ins = applyMention(text, 3, d, 'carol');
        expect(ins.text).toBe('@carol ');
        expect(ins.caret).toBe('@carol '.length);
    });

    test('no-op when detection is closed', () => {
        const ins = applyMention('hello', 5, {open: false, query: ''}, 'carol');
        expect(ins.text).toBe('hello');
        expect(ins.caret).toBe(5);
    });
});
