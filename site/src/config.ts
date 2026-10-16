// Brand and contact details. Set PUBLIC_* variables in Cloudflare Pages to
// override these defaults; they are inlined at build time.
export const SITE = {
  url: import.meta.env.PUBLIC_SITE_URL ?? 'https://myfayago.com',
  brand: import.meta.env.PUBLIC_BRAND_NAME ?? 'Faya',
  brandLine: 'BY XEGO',
  legalEntity: import.meta.env.PUBLIC_LEGAL_ENTITY_NAME ?? 'Xego Technologies Limited',
  supportEmail: import.meta.env.PUBLIC_SUPPORT_EMAIL ?? 'support@myfayago.com',
  privacyEmail: import.meta.env.PUBLIC_PRIVACY_EMAIL ?? 'privacy@myfayago.com',
  address:
    import.meta.env.PUBLIC_COMPANY_ADDRESS ??
    '221b Ikorodu Crescent, Dolphin Estate, Ikoyi, Lagos, Nigeria',
  whatsapp: import.meta.env.PUBLIC_WHATSAPP_LINK ?? '',
  metaDomainVerification: import.meta.env.PUBLIC_META_DOMAIN_VERIFICATION ?? '',
  tiktokVerification: import.meta.env.PUBLIC_TIKTOK_VERIFICATION ?? '',
} as const;
