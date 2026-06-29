// SETUP_DISMISSED_KEY is set when the user cancels the wizard, so the first-run
// redirect (which sends unconfigured installs to /setup) doesn't immediately
// bounce them back this session. Kept in its own module so the index route can
// import it without pulling in the full wizard component.
export const SETUP_DISMISSED_KEY = "claw.setupDismissed"
