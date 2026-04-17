import './globals.css';

export const metadata = {
  title: 'RelayFile Observer',
  description: 'File observer dashboard for relayfile workspaces',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className="bg-[#0a0a0b] text-[#fafafa] antialiased">
        <div className="min-h-screen">
          <header className="border-b border-[#27272a] px-6 py-4">
            <h1 className="text-xl font-semibold text-[#fafafa]">RelayFile Observer</h1>
          </header>
          {children}
        </div>
      </body>
    </html>
  );
}
