package common

// terms.go — the terms of service `drift account create` makes you read and
// accept before it ever sends a signup request.

// TermsVersion is the terms-of-service version this build of the CLI asks a
// new account to accept. It must equal cloud/platform's
// drift-common/models.TermsVersion — the server refuses a signup that names
// any other value, so a CLI and a platform that disagree here fail loudly
// at signup rather than silently recording acceptance of a version nobody
// published.
const TermsVersion = "2026-11-22"

// TermsURL is where the terms of service are published, incorporating the
// acceptable-use policy by reference.
const TermsURL = "https://docs.ondrift.eu/legal/terms/"
