# Wallet Admin Dashboard

React + Vite + Tailwind + shadcn/ui admin dashboard for the Mini Ledger & Wallet Service.

## Quick Start

```bash
npm install
npm run dev
```

The dashboard will be available at `http://localhost:5173`.

## Configuration

Set environment variables or configure in the UI:

- `VITE_API_URL`: API base URL (default: `http://localhost:8080`)
- `VITE_API_KEY`: API key for authentication (fallback: input in UI dialog)

## Pages

- **Accounts** (`/accounts`): List all accounts with cursor pagination
- **Account Detail** (`/accounts/:id`): Balance and paginated ledger entries
- **Transfers** (`/transfers`): List all transfers
- **Transfer Detail** (`/transfers/:id`): Transfer details with ledger entries
- **Reconciliation** (`/reconciliation`): Runs and discrepancies with filters
- **Integrity** (`/integrity`): Ledger health status

## Build

```bash
npm run build
```

Output: `dist/`
