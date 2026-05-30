// CartContext — single source of truth for the cart-item badge count.
//
// Previously every <CartIconBadge> ran its own 10s poll of the heavy
// /cart/summary endpoint. With the badge mounted on several retained tab
// screens (Home, Marketplace, …) that meant multiple /cart/summary hits every
// 10 seconds. This context instead holds one shared count: it loads once when
// auth is ready, then refreshes only when the cart actually changes, via
// cartService's change subscription (see cartService.subscribe). No polling.

import React, {
  createContext,
  useContext,
  useEffect,
  useState,
  useCallback,
  ReactNode,
} from "react";
import { useAuth } from "../AuthContext/AuthContext";
import { cartService } from "../components/services/cart";

interface CartContextType {
  /** Total quantity of items in the active cart (0 when signed out). */
  cartCount: number;
  /** Force an immediate re-read of the count from the backend. */
  refreshCartCount: () => Promise<void>;
}

const CartContext = createContext<CartContextType>({
  cartCount: 0,
  refreshCartCount: async () => {},
});

interface CartProviderProps {
  children: ReactNode;
}

export function CartProvider({ children }: CartProviderProps) {
  const { token } = useAuth();
  const [cartCount, setCartCount] = useState(0);

  const refreshCartCount = useCallback(async () => {
    if (!token) {
      setCartCount(0);
      return;
    }
    try {
      cartService.setToken(token);
      const count = await cartService.getCartItemCount();
      setCartCount(count);
    } catch {
      // Badge is non-critical; keep the last known count on failure.
    }
  }, [token]);

  // Initial load, and reload whenever auth changes (login / logout / refresh).
  useEffect(() => {
    refreshCartCount();
  }, [refreshCartCount]);

  // Event-driven refresh: cartService notifies on every add/update/remove/
  // clear/checkout/restore, so the badge stays current without polling.
  useEffect(() => {
    const unsubscribe = cartService.subscribe(() => {
      refreshCartCount();
    });
    return unsubscribe;
  }, [refreshCartCount]);

  return (
    <CartContext.Provider value={{ cartCount, refreshCartCount }}>
      {children}
    </CartContext.Provider>
  );
}

export function useCartCount() {
  return useContext(CartContext);
}

export default CartContext;
