# Aegis web (Next.js)

The reviewer-facing app for AegisSDLC. It runs on Vercel and talks to the Aegis Go
API on AWS **server-side only** (the browser never sees the API or its token):

    browser ──HTTPS──▶ Next.js on Vercel ──HTTPS + service token──▶ CloudFront ──▶ Aegis API (EC2)

## Environment variables (Vercel → Settings → Environment Variables)

| Name | Value |
|---|---|
| `AEGIS_API_URL` | the CloudFront URL printed by `scripts/aws.sh public` |
| `AEGIS_BFF_TOKEN` | the service token (`scripts/aws.sh vercel-env` prints it) |
| `AEGIS_SESSION_SECRET` | 64 random hex characters (signs the session cookie) |
| `AEGIS_ACCESS_CODE` | a long code you give to reviewers (gates the demo sign-in) |

## Local

    cp .env.example .env.local   # point AEGIS_API_URL at http://127.0.0.1:8480
    npm install && npm run dev
