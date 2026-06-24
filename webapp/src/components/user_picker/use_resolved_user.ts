// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// useResolvedUser resolves a user id to a display label (@username, or the
// user's name) for showing assignees in the UI. The host webapp caches user
// profiles in the Redux store (state.entities.users.users[id]); we read that
// first so most lookups are synchronous and free. When the user isn't cached
// (e.g. an assignee from another channel), we fetch it once via the REST API
// and fall back to the raw id until it lands.
//
// Returns '' when userID is empty, and the raw id as a last-resort label so the
// UI never renders a blank — the caller can still show *something* actionable.

import * as client from "client";
import { useEffect, useState } from "react";
import { useSelector } from "react-redux";

import type { GlobalState } from "@mattermost/types/store";
import type { UserProfile } from "@mattermost/types/users";

// Host Redux store slice shape for users (state.entities.users).
interface UsersSlice {
	users?: Record<string, UserProfile>;
}

type GlobalStateWithUsers = GlobalState & { entities?: { users?: UsersSlice } };

// selectUser reads a cached profile from the host Redux store. Returns
// undefined when the user isn't cached or the store shape is unexpected.
function selectUser(
	state: GlobalStateWithUsers,
	userID: string,
): UserProfile | undefined {
	return state.entities?.users?.users?.[userID];
}

// labelFor derives a display label from a profile: "@username" (most compact
// and unambiguous in a chat context), falling back to the id.
function labelFor(user: UserProfile | undefined, userID: string): string {
	if (!user) {
		return userID;
	}
	if (user.username) {
		return "@" + user.username;
	}
	return userID;
}

export function useResolvedUser(userID: string): {
	label: string;
	user: UserProfile | undefined;
} {
	const storeUser = useSelector((state: GlobalStateWithUsers) =>
		selectUser(state, userID),
	);
	const [fetched, setFetched] = useState<UserProfile | undefined>(undefined);

	// Fetch only when the store doesn't have the user and we haven't fetched
	// already for this id. Reset fetched when the id changes.
	useEffect(() => {
		setFetched(undefined);
		if (!userID || storeUser) {
			return undefined;
		}
		let cancelled = false;
		client
			.getUser(userID)
			.then((u) => {
				if (!cancelled && u) {
					// Normalize to a minimal UserProfile shape for labelFor.
					setFetched({ id: u.id, username: u.username } as UserProfile);
				}
			})
			.catch(() => {
				// Leave fetched undefined; we fall back to the raw id.
			});
		return () => {
			cancelled = true;
		};
	}, [userID, storeUser]);

	const user = storeUser ?? fetched;
	return { label: userID ? labelFor(user, userID) : "", user };
}

// useResolvedUsers resolves a set of user ids in one hook (for lists). Returns
// a map id → label where a *missing* key means "still resolving" (the caller
// should show a muted placeholder, NOT the raw id — that avoids flashing an
// opaque value). Resolved users map to "@username".
//
// NOTE: unlike useResolvedUser (single id), this batch hook does NOT consult
// the host Redux store — it fetches every id via client.getUser. That is a
// deliberate trade-off: subscribing the whole users slice for a list would be
// wasteful, and the list view tolerates the brief fetch latency behind the
// muted placeholder. If a list is dominated by already-cached users, prefer
// useResolvedUser per row or wire a store read here.
export function useResolvedUsers(userIDs: string[]): Record<string, string> {
	const [labels, setLabels] = useState<Record<string, string>>({});

	useEffect(() => {
		let cancelled = false;
		const unique = Array.from(new Set(userIDs.filter(Boolean)));

		// Do NOT seed the map with raw ids — that would flash the opaque id
		// before the name resolves. Only entries that resolve successfully are
		// added; absent keys mean "still loading".
		Promise.all(unique.map((id) => client.getUser(id).catch(() => null))).then(
			(results) => {
				if (cancelled) {
					return;
				}
				setLabels((prev) => {
					const merged = { ...prev };
					results.forEach((u, i) => {
						const id = unique[i];
						if (u && u.username) {
							merged[id] = "@" + u.username;
						}
					});
					return merged;
				});
			},
		);

		return () => {
			cancelled = true;
		};

		// Depend on the joined id list so a changed set re-resolves.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [userIDs.join(",")]);

	return labels;
}

// useResolvedStatuses resolves a set of user ids to their presence status
// (online/away/dnd/offline) via client.getUserStatus (host
// /api/v4/users/<id>/status). Returns a map id → status; a *missing* key
// means "not resolved yet" and the caller falls back to the offline modifier
// (explicit, data-driven — never a dead default). Used by the Task Details
// Activity feed to drive the avatar status-dot modifier class (AC5/AC6,
// task-details-panel styling).
export function useResolvedStatuses(userIDs: string[]): Record<string, string> {
	const [statuses, setStatuses] = useState<Record<string, string>>({});

	useEffect(() => {
		let cancelled = false;
		const unique = Array.from(new Set(userIDs.filter(Boolean)));

		Promise.all(
			unique.map((id) => client.getUserStatus(id).catch(() => null)),
		).then((results) => {
			if (cancelled) {
				return;
			}
			setStatuses((prev) => {
				const merged = { ...prev };
				results.forEach((s, i) => {
					const id = unique[i];
					if (s && s.status) {
						merged[id] = s.status;
					}
				});
				return merged;
			});
		});

		return () => {
			cancelled = true;
		};

		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [userIDs.join(",")]);

	return statuses;
}
