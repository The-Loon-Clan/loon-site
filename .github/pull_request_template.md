## What changed, and why it was wrong before

<!-- The "why" is the part that is hard to reconstruct later. If you found the
     problem by measuring something, put the measurement in. -->

## How you know it works

<!-- `make check` is the floor, not the answer. For anything on the request
     path, say what you actually exercised: a page you loaded, a row you looked
     at in the database, a flow you ran end to end.

     If you fixed a bug, did you see the test fail against the old code? -->

- [ ] `make check` passes (gofmt, build, vet, sqllint, tests, coverage floor)
- [ ] Storage changes: `make itest` run against a real database
- [ ] Behaviour verified by running it, not only by compiling it
