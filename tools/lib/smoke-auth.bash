# shellcheck shell=bash
# shellcheck disable=SC2034 # the smokes that source this read these
# The credentials the gateway smokes send (tools/smoke-core-services,
# tools/smoke-dashboard), for a gateway whose legacy access is off, as a new
# install's (docs/install/accounts.md):
#  - LIFE_OS_SMOKE_TOKEN: a personal API token (lo_tok_…, Settings → API
#    tokens), sent as Authorization: Bearer to the services' routes. Mark
#    it a development tool, so that its writes are its own (dev:<name>),
#    never the owner's;
#  - LIFE_OS_SMOKE_COOKIE: a session cookie (name=value) for the
#    Dashboard's pages, which a token never opens.
# Unset, the smokes send none, as under legacy access (the verification
# stack's default).
smoke_api_auth=()
smoke_page_auth=()
if [[ -n ${LIFE_OS_SMOKE_TOKEN:-} ]]; then
  smoke_api_auth=(-H "authorization: Bearer ${LIFE_OS_SMOKE_TOKEN}")
fi
if [[ -n ${LIFE_OS_SMOKE_COOKIE:-} ]]; then
  smoke_page_auth=(-H "cookie: ${LIFE_OS_SMOKE_COOKIE}")
fi
