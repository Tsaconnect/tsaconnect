# TSA API

Express.js REST API with MongoDB (Mongoose) backend for a trading/asset management platform.

## Commands

```bash
npm start          # Start server (node app.js), default port 5000
npm install        # Install dependencies
```

No test runner configured yet (`npm test` exits with error).

## Architecture

```
app.js              # Entry point - Express setup, middleware, route mounting, error handling
config/
  database.js       # MongoDB connection (uses MONGODB_URI env var) [gitignored]
  cloudinary.js     # Cloudinary config for image uploads
controllers/        # Route handlers (asset, buy, cart, category, market, portfolio, product, transaction, user, verification)
middleware/
  auth.js           # JWT Bearer token auth - attaches req.user, checks accountStatus
  adminAuth.js      # Admin role check
  cloudinaryUpload.js # Multer + Cloudinary image upload pipeline
models/             # Mongoose schemas (Asset, Cart, Category, Portfolio, Product, Transaction, User, VerificationLog, Wallet)
routes/             # Express routers mounted at /api/*
services/           # Business logic (blockchainService, cartService, priceService)
utils/
  validators.js     # Shared validation helpers
```

## API Routes

All routes prefixed with `/api/`:
- `/auth` - signup (3-step: registration, identity docs, facial verification), login
- `/users` - user profile management
- `/verification` - document/identity verification
- `/upload` - image uploads via Cloudinary
- `/assets`, `/transactions`, `/portfolio`, `/market` - trading/asset features
- `/products`, `/cart` - marketplace/e-commerce features
- `/health` - health check (root level, not under /api)

## Key Patterns

- **Response format**: All responses use `{ success: boolean, message: string, data?: object }` or `{ success: false, errors: array }`
- **Auth**: JWT tokens with 7-day expiry. Token in `Authorization: Bearer <token>` header. Auth middleware at `middleware/auth.js`
- **Validation**: express-validator inline in route definitions (not extracted to separate files)
- **File uploads**: Multer -> Sharp (image processing) -> Cloudinary. Max 5MB
- **Rate limiting**: 100 requests per 15 minutes per IP on all `/api/` routes
- **Error handling**: Centralized error middleware in app.js handles Mongoose, JWT, Multer errors
- **CommonJS modules** throughout (`require`/`module.exports`)

## Environment Variables

Required (set in `.env`, gitignored):
- `MONGODB_URI` - MongoDB connection string
- `JWT_SECRET` - JWT signing secret
- `FRONTEND_URL` - CORS allowed origin (defaults to `http://localhost:3000`)
- `PORT` - Server port (defaults to `5000`)
- `NODE_ENV` - `development` or `production`
- Cloudinary: `CLOUDINARY_CLOUD_NAME`, `CLOUDINARY_API_KEY`, `CLOUDINARY_API_SECRET`

## Gotchas

- `config/database.js` is gitignored - must be created locally
- Express 5 is used (v5.2.1), not Express 4 - some API differences
- The 404 handler uses a middleware function (not `app.all('*')`) due to Express 5 routing changes
- `cartRoutes.js` filename differs from other route files (camelCase vs lowercase)
- Verification process in auth routes uses a simulated `setTimeout` - not production-ready
