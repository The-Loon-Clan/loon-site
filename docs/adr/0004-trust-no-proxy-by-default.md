# 4. Believe no proxy unless told which one

## Status

Accepted.

## Context

gin trusts every proxy unless configured otherwise, and `SetTrustedProxies` was
never called. `c.ClientIP()` therefore returned whatever the caller put in
`X-Forwarded-For` — a request header like any other, settable by anyone who can
reach the port.

That value is not decorative. It is recorded in `login_logs`, which is the page
a member opens to check whether somebody else has been in their account, and the
record an admin reads after a breach. It is also passed to the captcha as the
client address.

Measured before fixing: two logins from one machine, sending two invented
values, recorded **two different addresses**.

## Decision

Trust nothing by default. `LOON_TRUSTED_PROXIES` names the proxy — an address or
CIDR — when there is one.

Behind a proxy, name **one address**, not the subnet. gin walks the header from
the right and stops at the first address it does not trust:

- Trust only the proxy → the walk stops at the value the proxy appended, which
  is the real peer. Anything the client invented sits harmlessly to its left.
- Trust the whole subnet → the walk continues *past* the real peer into the
  invented part. Putting a proxy in front then restores the spoofing it was
  added to prevent.

`compose.lb.yml` pins nginx to a fixed address precisely so it can be named.

## Consequences

**Failing closed is visible; failing open is not.** A deployment that forgets to
set this logs the proxy's own address for everybody — useless, and obvious the
first time anybody looks. The previous behaviour was useless *and looked
correct*.

**It is a breaking change for anyone already behind a proxy**, and is called out
in the changelog and the upgrade notes rather than left to be discovered.

**The compose network needs a fixed subnet**, because an address that cannot be
predicted cannot be named. That is a small operational constraint arriving from
a security decision, which is worth knowing when reading the compose file.

**Verified both ways, and both are necessary.** Three spoofed values from one
source collapse to one address; two genuinely different sources still record
two. A test that only checked the first would pass just as well against a
configuration that logged the proxy's address for every request on earth.
