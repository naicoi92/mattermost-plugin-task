// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// MetaDropdown is the click-to-select dropdown used by the Task Detail and
// New Task meta-tables for Status and Priority. It replaces the old
// click-to-cycle chips: clicking the trigger opens a small listbox panel; the
// user picks the value explicitly instead of advancing through a cycle.
//
// The trigger is caller-supplied (triggerNode) so each field keeps its own
// visual — Status renders a StatusPill, Priority renders a dot + label. The
// dropdown only owns open/close, outside-click dismiss, Esc, and the option
// list. Mirrors the `.dd` control in the design (mattermost-task-sidebar).

import React, {useEffect, useRef, useState} from 'react';

export interface MetaDropdownOption {
    value: string;
    label: string;
}

export interface MetaDropdownProps {

    // options is the full list of selectable values, in display order.
    options: MetaDropdownOption[];

    // value is the currently selected option value.
    value: string;

    // onChange fires when the user picks an option (never for the already-
    // selected one).
    onChange: (value: string) => void;

    // triggerNode renders the chip-like control shown in the closed state. It
    // should reflect the current value (e.g. a StatusPill, or a dot + label).
    triggerNode: React.ReactNode;

    // ariaLabel is the accessible name for the dropdown trigger.
    ariaLabel: string;

    // align places the panel on the left ("start", default) or right ("end")
    // of the trigger — useful when the dropdown sits near the RHS edge.
    align?: 'start' | 'end';

    // disabled disables the trigger (the panel won't open).
    disabled?: boolean;
}

export default function MetaDropdown({options, value, onChange, triggerNode, ariaLabel, align = 'start', disabled}: MetaDropdownProps): JSX.Element {
    const [open, setOpen] = useState(false);
    const rootRef = useRef<HTMLDivElement>(null);

    // Close on outside click while open.
    useEffect(() => {
        if (!open) {
            return undefined;
        }
        const onDocClick = (e: MouseEvent) => {
            if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
                setOpen(false);
            }
        };
        const onKey = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                setOpen(false);
            }
        };
        document.addEventListener('mousedown', onDocClick);
        document.addEventListener('keydown', onKey);
        return () => {
            document.removeEventListener('mousedown', onDocClick);
            document.removeEventListener('keydown', onKey);
        };
    }, [open]);

    const choose = (v: string) => {
        setOpen(false);
        if (v !== value) {
            onChange(v);
        }
    };

    return (
        <div
            className={`meta-dropdown ${open ? 'meta-dropdown--open' : ''}`}
            ref={rootRef}
        >
            <button
                type='button'
                className={`meta-dropdown__trigger ${open ? 'meta-dropdown__trigger--open' : ''}`}
                onClick={() => !disabled && setOpen((v) => !v)}
                aria-haspopup='listbox'
                aria-expanded={open}
                aria-label={ariaLabel}
                disabled={disabled}
            >
                {triggerNode}
                <svg
                    className='meta-dropdown__caret'
                    viewBox='0 0 16 16'
                    aria-hidden='true'
                >
                    <path
                        d='M4 6l4 4 4-4'
                        fill='none'
                        stroke='currentColor'
                        strokeWidth='1.6'
                        strokeLinecap='round'
                        strokeLinejoin='round'
                    />
                </svg>
            </button>

            {open && (
                <div
                    className={`meta-dropdown__panel meta-dropdown__panel--${align}`}
                    role='listbox'
                >
                    {options.map((opt) => (
                        <button
                            key={opt.value}
                            type='button'
                            className='meta-dropdown__option'
                            onClick={() => choose(opt.value)}
                            role='option'
                            aria-selected={opt.value === value}
                        >
                            <span className='meta-dropdown__option-label'>{opt.label}</span>
                            <svg
                                className='meta-dropdown__option-check'
                                viewBox='0 0 16 16'
                                aria-hidden={opt.value === value ? 'false' : 'true'}
                                style={{visibility: opt.value === value ? 'visible' : 'hidden'}}
                            >
                                <path
                                    d='M3 8.5L6.5 12 13 4.5'
                                    fill='none'
                                    stroke='currentColor'
                                    strokeWidth='2'
                                    strokeLinecap='round'
                                    strokeLinejoin='round'
                                />
                            </svg>
                        </button>
                    ))}
                </div>
            )}
        </div>
    );
}
