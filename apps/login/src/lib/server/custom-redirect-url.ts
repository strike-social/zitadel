"use server";

import { cookies } from "next/headers";

const REDIRECT_URL_COOKIE_NAME = "custom:redirectUrl";

export async function setCustomRedirectUrl(redirectUrl: string) {
  const cookieStore = await cookies();

  cookieStore.set(REDIRECT_URL_COOKIE_NAME, redirectUrl, { httpOnly: true });
}

export async function getAndDeleteCustomRedirectUrl(): Promise<string | undefined> {
  const cookieStore = await cookies();

  const redirectUrl = cookieStore.get(REDIRECT_URL_COOKIE_NAME)?.value;

  if (redirectUrl) {
    cookieStore.delete(REDIRECT_URL_COOKIE_NAME);
  }

  return redirectUrl;
}
