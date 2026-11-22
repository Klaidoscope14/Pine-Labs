import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Aegis", template: "%s · Aegis" },
  description: "Governed AI change control for payments code",
  icons: { icon: "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M8 1l6 2.5v4.2C14 11.5 11.4 14.3 8 15c-3.4-.7-6-3.5-6-7.3V3.5z' fill='%235e6ad2'/%3E%3C/svg%3E" },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (<html lang="en"><body>{children}</body></html>);
}
