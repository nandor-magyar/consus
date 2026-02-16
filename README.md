# Consus

Simple media file server. Built to share rehearsal recordings for [European Onion](https://www.instagram.com/europeanonionofficial/) -- because Dropbox and Google Drive is someone else's computer for no reason.

Heavily depends on the browser's own HTML5 player. No JavaScript frameworks were harmed in the making of this.

## Setup

### Google OAuth

Pages are public. Commenting is not -- you need to be on the list.

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a project (or pick an existing one)
3. **APIs & Services > Credentials** > **Create Credentials** > **OAuth client ID** > **Web application**
4. Add your redirect URI (e.g. `http://localhost:7001/callback`)
5. Grab the **Client ID** and **Client Secret**

### Environment

```sh
export GOOGLE_CLIENT_ID="your-client-id.apps.googleusercontent.com"
export GOOGLE_CLIENT_SECRET="your-client-secret"
export GOOGLE_REDIRECT_URL="http://localhost:7001/callback"
export ALLOWED_EMAILS="user1@gmail.com,user2@gmail.com"
```

Or throw them in a `.env` file -- the Makefile picks it up.

### Run

```sh
make run
```

No OAuth env vars? The login link still shows up but goes nowhere. Only emails in `ALLOWED_EMAILS` get to comment.

## TODO

- Refactor (htmx, cleaner split between backend and UI)
- Tests (someday)

