import '@testing-library/jest-dom/vitest';
import { configure } from '@testing-library/react';

// findBy* waits 1 s by default; on a busy machine a page can take longer to answer.
configure({ asyncUtilTimeout: 3000 });
