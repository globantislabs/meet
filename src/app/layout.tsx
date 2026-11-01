import type { Metadata } from "next";
import { Space_Grotesk, Instrument_Serif, Geist_Mono } from "next/font/google";
import "./globals.css";
import { Toaster } from "@/components/ui/toaster";

// Body + UI font — Space Grotesk: the everyday voice. Slightly technical,
// quietly friendly, extremely legible at UI sizes. Never mixed with more
// than one other family in a view (sans + mono; display sparingly).
const spaceGrotesk = Space_Grotesk({
  variable: "--font-space",
  subsets: ["latin"],
  display: "swap",
});

// Display font — Instrument Serif: editorial contrast, one moment per view
// at most (hero lines, recap titles). Not for UI chrome.
const instrumentSerif = Instrument_Serif({
  variable: "--font-instrument",
  subsets: ["latin"],
  weight: "400",
  style: ["normal", "italic"],
  display: "swap",
});

// Mono font — Geist Mono: data only. Room codes, timers, counts, metrics,
// micro-labels that label data. Room codes get wide letter-spacing so
// characters are individually readable.
const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
  display: "swap",
});

export const metadata: Metadata = {
  title: "VoiceBridge — Multilingual Group Voice Translator",
  description:
    "Browser-based group voice room where every participant speaks their own language and hears every other speaker translated into their own preferred language. LiveKit WebRTC + server-side speech-to-speech translation.",
  keywords: ["voice translation", "multilingual", "LiveKit", "WebRTC", "speech-to-speech", "group call"],
  authors: [{ name: "VoiceBridge" }],
  icons: {
    icon: "https://z-cdn.chatglm.cn/z-ai/static/logo.svg",
  },
  openGraph: {
    title: "VoiceBridge — Multilingual Group Voice Translator",
    description: "Everyone speaks their language. Everyone hears their own.",
    siteName: "VoiceBridge",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: "VoiceBridge — Multilingual Group Voice Translator",
    description: "Everyone speaks their language. Everyone hears their own.",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body
        className={`${spaceGrotesk.variable} ${instrumentSerif.variable} ${geistMono.variable} antialiased bg-background text-foreground`}
      >
        {children}
        <Toaster />
      </body>
    </html>
  );
}
