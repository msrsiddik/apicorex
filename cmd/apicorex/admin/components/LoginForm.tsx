"use client";

import { useState } from "react";
import { login } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { KeyRound } from "lucide-react";

export default function LoginForm({ onSuccess }: { onSuccess: () => void }) {
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(key);
      onSuccess();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex h-screen items-center justify-center">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-accent text-accent-foreground">
            <KeyRound className="h-5 w-5" />
          </div>
          <CardTitle className="pt-2">ApiCoreX Gateway</CardTitle>
          <CardDescription>Enter the dashboard key to continue.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="flex flex-col gap-3">
            <Input
              type="password"
              autoFocus
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder="Dashboard key"
            />
            {error && <p className="text-sm text-danger">{error}</p>}
            <Button type="submit" disabled={busy || !key}>
              {busy ? "Checking…" : "Enter"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
