import { ThemeProvider } from '@datum-cloud/datum-ui/theme';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {/* The demo has no theme switch of its own: it follows the screen it is
        shown on, so a booth display and a laptop in a lit room each get the
        scheme their owner already chose. */}
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
      <App />
    </ThemeProvider>
  </StrictMode>
);
