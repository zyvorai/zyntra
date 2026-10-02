import { createContext, useContext } from 'react';
import type { WhoAmI } from './api';

export const WhoContext = createContext<WhoAmI | null>(null);

export const useWho = () => useContext(WhoContext);
