/**
 * Services reject writes that do not say who makes them (`actor_required`).
 * The Dashboard always acts for the owner, so its service clients send
 * `X-Life-OS-Actor: user` on every request.
 */
export const ACTOR_HEADER = "X-Life-OS-Actor";

export const OWNER_HEADERS = { [ACTOR_HEADER]: "user" } as const;

/**
 * Actors a browser may claim, as at the gateway: the owner or a development
 * tool. Internal actors (`agent:`, `system:`) never come from outside.
 */
export const PUBLIC_ACTOR = /^(?:user|dev:[A-Za-z0-9._-]{1,64})$/;
