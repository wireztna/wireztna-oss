"""Email service for sending OTP codes and onboarding emails."""

import logging
import smtplib
from email.mime.text import MIMEText
from email.mime.multipart import MIMEMultipart

from app.config import settings

logger = logging.getLogger(__name__)


def _onboarding_from() -> str:
    """Build the onboarding From header from SMTP settings.

    Falls back to just the display name when no SMTP_FROM address is configured.
    """
    name = settings.smtp_from_name or "WireZTNA"
    if settings.smtp_from:
        return f"{name} <{settings.smtp_from}>"
    return name


def _send_email(to_email: str, subject: str, text_body: str, html_body: str, from_header: str | None = None) -> bool:
    """Low-level email sending. Returns True on success."""
    if not settings.smtp_host:
        logger.error("SMTP not configured — cannot send email")
        return False

    msg = MIMEMultipart("alternative")
    msg["Subject"] = subject
    msg["From"] = from_header or f"{settings.smtp_from_name} <{settings.smtp_from}>"
    msg["To"] = to_email
    msg.attach(MIMEText(text_body, "plain"))
    msg.attach(MIMEText(html_body, "html"))

    try:
        if settings.smtp_port == 465:
            with smtplib.SMTP_SSL(settings.smtp_host, settings.smtp_port, timeout=10) as server:
                if settings.smtp_user:
                    server.login(settings.smtp_user, settings.smtp_password)
                server.send_message(msg)
        else:
            with smtplib.SMTP(settings.smtp_host, settings.smtp_port, timeout=10) as server:
                if settings.smtp_use_tls:
                    server.starttls()
                if settings.smtp_user:
                    server.login(settings.smtp_user, settings.smtp_password)
                server.send_message(msg)

        logger.info(f"Email sent to {to_email}: {subject}")
        return True

    except Exception as e:
        logger.error(f"Failed to send email to {to_email}: {e}")
        return False


def send_otp_email(to_email: str, code: str, username: str) -> bool:
    """Send an OTP code to the user's email address."""
    subject = f"WireZTNA — Tu codigo de acceso: {code}"

    text_body = (
        f"Hola {username},\n\n"
        f"Tu codigo de acceso es: {code}\n\n"
        f"Este codigo es valido durante {settings.otp_ttl_seconds // 60} minutos.\n"
        f"Si no has solicitado este codigo, ignora este mensaje.\n\n"
        f"— WireZTNA"
    )

    html_body = f"""
    <div style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; max-width: 480px; margin: 0 auto; padding: 32px;">
        <h2 style="color: #1a1a2e; margin-bottom: 8px;">WireZTNA</h2>
        <p style="color: #555; margin-bottom: 24px;">Hola {username},</p>
        <p style="color: #555; margin-bottom: 16px;">Tu codigo de acceso:</p>
        <div style="background: #f0f4ff; border: 1px solid #d0d8f0; border-radius: 8px; padding: 20px; text-align: center; margin-bottom: 24px;">
            <span style="font-size: 32px; font-weight: 700; letter-spacing: 6px; color: #1a1a2e;">{code}</span>
        </div>
        <p style="color: #888; font-size: 13px;">
            Valido durante {settings.otp_ttl_seconds // 60} minutos. Si no has solicitado este codigo, ignora este mensaje.
        </p>
    </div>
    """

    return _send_email(to_email, subject, text_body, html_body)


def send_onboarding_email(to_email: str, username: str, portal_url: str, temp_password: str = "") -> bool:
    """Send a professional onboarding email inviting the user to the platform.
    
    The "Open My Portal" button includes auto-login params (email + temp_password)
    so the user lands authenticated and is prompted to change password immediately.
    """
    import urllib.parse
    subject = "Welcome to WireZTNA — Your secure network access is ready"

    # Build portal link with auto-login params for one-click access
    if temp_password:
        portal_link = f"{portal_url}/login?auto_email={urllib.parse.quote(to_email)}&auto_pass={urllib.parse.quote(temp_password)}"
    else:
        portal_link = f"{portal_url}/portal"

    password_section_text = ""
    if temp_password:
        password_section_text = """
ACTIVATE YOUR ACCOUNT
=====================
Click the link below to set your password and activate your account:

""" + portal_link + """

This link will log you in automatically and ask you to set a password.

"""

    text_body = f"""Hello {username},

Your administrator has set up secure network access for you using WireZTNA.
{password_section_text}
GETTING STARTED
===============

1. Activate your account
   Click the link above (or the button in this email) to set your password.

2. Download the client
   Visit: {portal_url}/downloads
   Available for macOS, Linux, and Windows.

3. Enroll your device
   Open a terminal and run:
   wireztna enroll "<your_enrollment_url>"

   Your admin will provide the enrollment URL, or you can generate one
   from your portal (Portal > Devices > New Device Token).

4. Sign in
   wireztna login
   Enter your email ({to_email}) — a 6-digit code will be sent to this address.

5. Connect
   sudo wireztna connect
   That's it — you're connected to your private networks.

YOUR PORTAL
============
Access your self-service portal at:
{portal_link}

From there you can:
- Change your password (recommended after first login)
- View your assigned networks and access policies
- Manage your devices (generate enrollment tokens)
- Enable MFA (authenticator app) for extra security

NEED HELP?
==========
Contact your administrator if you have questions about your access
or encounter any issues during setup.

— The WireZTNA Team
"""

    # Build the activation button block (conditional)
    if temp_password:
        activate_block = f"""
              <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="margin-bottom: 28px;">
                <tr>
                  <td align="center">
                    <!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="{portal_link}" style="height:48px;v-text-anchor:middle;width:260px;" arcsize="16%" strokecolor="#4f46e5" fillcolor="#4f46e5"><w:anchorlock/><center style="color:#ffffff;font-family:Segoe UI,Arial,sans-serif;font-size:15px;font-weight:600;">Activate My Account</center></v:roundrect><![endif]-->
                    <!--[if !mso]><!-->
                    <a href="{portal_link}" style="display: inline-block; background-color: #4f46e5; color: #ffffff; text-decoration: none; padding: 15px 36px; border-radius: 8px; font-size: 15px; font-weight: 600; font-family: Segoe UI, Arial, sans-serif;">Activate My Account</a>
                    <!--<![endif]-->
                  </td>
                </tr>
                <tr>
                  <td align="center" style="padding-top: 12px;">
                    <p style="margin: 0; font-size: 12px; color: #888888; font-family: Segoe UI, Arial, sans-serif;">You will be asked to set a password on first access.</p>
                  </td>
                </tr>
              </table>
"""
    else:
        activate_block = ""

    html_body = f"""
<!DOCTYPE html>
<html xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->
</head>
<body style="margin: 0; padding: 0; background-color: #f5f7fa; font-family: 'Segoe UI', Arial, sans-serif; -webkit-font-smoothing: antialiased;">
  <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background-color: #f5f7fa;">
    <tr>
      <td align="center" style="padding: 40px 20px;">
        <table role="presentation" cellpadding="0" cellspacing="0" width="600" style="max-width: 600px; width: 100%;">

          <!-- Header -->
          <tr>
            <td align="center" bgcolor="#4f46e5" style="background-color: #4f46e5; padding: 32px 40px; border-radius: 12px 12px 0 0;">
              <!--[if mso]><v:rect xmlns:v="urn:schemas-microsoft-com:vml" fill="true" stroke="false" style="width:600px;height:90px;"><v:fill type="solid" color="#4f46e5"/><v:textbox inset="0,0,0,0" style="mso-fit-shape-to-text:true;"><![endif]-->
              <h1 style="color: #ffffff; margin: 0; font-size: 24px; font-weight: 700; font-family: 'Segoe UI', Arial, sans-serif;">WireZTNA</h1>
              <p style="color: #e0e0ff; margin: 8px 0 0; font-size: 14px; font-family: 'Segoe UI', Arial, sans-serif;">Zero Trust Network Access</p>
              <!--[if mso]></v:textbox></v:rect><![endif]-->
            </td>
          </tr>

          <!-- Body -->
          <tr>
            <td bgcolor="#ffffff" style="background-color: #ffffff; padding: 40px; border-radius: 0 0 12px 12px;">

              <p style="color: #1a1a2e; font-size: 16px; margin: 0 0 8px; font-family: 'Segoe UI', Arial, sans-serif;">Hello {username},</p>
              <p style="color: #555555; font-size: 14px; line-height: 1.6; margin: 0 0 28px; font-family: 'Segoe UI', Arial, sans-serif;">
                Your administrator has set up secure network access for you. Click the button below to activate your account and set your password.
              </p>

              <!-- CTA Button — Activate Account (placed prominently before steps) -->
              {activate_block}

              <!-- Steps -->
              <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="margin-bottom: 32px;">
                <tr>
                  <td width="36" valign="top" style="padding-bottom: 18px;">
                    <table role="presentation" cellpadding="0" cellspacing="0"><tr><td bgcolor="#4f46e5" width="28" height="28" align="center" style="background-color: #4f46e5; border-radius: 14px; color: #ffffff; font-size: 13px; font-weight: 700; font-family: Arial, sans-serif;">1</td></tr></table>
                  </td>
                  <td valign="top" style="padding-bottom: 18px; padding-left: 10px;">
                    <p style="margin: 0; font-size: 14px; font-weight: 600; color: #1a1a2e; font-family: 'Segoe UI', Arial, sans-serif;">Download the client</p>
                    <p style="margin: 4px 0 0; font-size: 13px; color: #666666; font-family: 'Segoe UI', Arial, sans-serif;">Available for macOS, Linux, and Windows.</p>
                  </td>
                </tr>
                <tr>
                  <td width="36" valign="top" style="padding-bottom: 18px;">
                    <table role="presentation" cellpadding="0" cellspacing="0"><tr><td bgcolor="#4f46e5" width="28" height="28" align="center" style="background-color: #4f46e5; border-radius: 14px; color: #ffffff; font-size: 13px; font-weight: 700; font-family: Arial, sans-serif;">2</td></tr></table>
                  </td>
                  <td valign="top" style="padding-bottom: 18px; padding-left: 10px;">
                    <p style="margin: 0; font-size: 14px; font-weight: 600; color: #1a1a2e; font-family: 'Segoe UI', Arial, sans-serif;">Enroll your device</p>
                    <p style="margin: 4px 0 0; font-size: 13px; color: #666666; font-family: 'Segoe UI', Arial, sans-serif;">Run: <code style="background-color: #f0f4ff; padding: 2px 6px; font-size: 12px; font-family: Consolas, monospace;">wireztna enroll "&lt;url&gt;"</code></p>
                    <p style="margin: 4px 0 0; font-size: 12px; color: #888888; font-family: 'Segoe UI', Arial, sans-serif;">Generate a token from Portal &gt; Devices, or ask your admin.</p>
                  </td>
                </tr>
                <tr>
                  <td width="36" valign="top" style="padding-bottom: 18px;">
                    <table role="presentation" cellpadding="0" cellspacing="0"><tr><td bgcolor="#4f46e5" width="28" height="28" align="center" style="background-color: #4f46e5; border-radius: 14px; color: #ffffff; font-size: 13px; font-weight: 700; font-family: Arial, sans-serif;">3</td></tr></table>
                  </td>
                  <td valign="top" style="padding-bottom: 18px; padding-left: 10px;">
                    <p style="margin: 0; font-size: 14px; font-weight: 600; color: #1a1a2e; font-family: 'Segoe UI', Arial, sans-serif;">Sign in</p>
                    <p style="margin: 4px 0 0; font-size: 13px; color: #666666; font-family: 'Segoe UI', Arial, sans-serif;">Run: <code style="background-color: #f0f4ff; padding: 2px 6px; font-size: 12px; font-family: Consolas, monospace;">wireztna login</code> — enter your email, a code arrives here.</p>
                  </td>
                </tr>
                <tr>
                  <td width="36" valign="top">
                    <table role="presentation" cellpadding="0" cellspacing="0"><tr><td bgcolor="#4f46e5" width="28" height="28" align="center" style="background-color: #4f46e5; border-radius: 14px; color: #ffffff; font-size: 13px; font-weight: 700; font-family: Arial, sans-serif;">4</td></tr></table>
                  </td>
                  <td valign="top" style="padding-left: 10px;">
                    <p style="margin: 0; font-size: 14px; font-weight: 600; color: #1a1a2e; font-family: 'Segoe UI', Arial, sans-serif;">Connect</p>
                    <p style="margin: 4px 0 0; font-size: 13px; color: #666666; font-family: 'Segoe UI', Arial, sans-serif;">Run: <code style="background-color: #f0f4ff; padding: 2px 6px; font-size: 12px; font-family: Consolas, monospace;">sudo wireztna connect</code> — you're in.</p>
                  </td>
                </tr>
              </table>

              <!-- Secondary link to portal (after steps) -->
              <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="margin-bottom: 32px;">
                <tr>
                  <td align="center">
                    <!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="{portal_url}/portal" style="height:40px;v-text-anchor:middle;width:180px;" arcsize="20%" strokecolor="#e0e0e0" fillcolor="#f8f9fc"><w:anchorlock/><center style="color:#4f46e5;font-family:'Segoe UI',Arial,sans-serif;font-size:13px;font-weight:600;">Go to Portal</center></v:roundrect><![endif]-->
                    <!--[if !mso]><!-->
                    <a href="{portal_url}/portal" style="display: inline-block; background-color: #f8f9fc; color: #4f46e5; text-decoration: none; padding: 11px 24px; border-radius: 8px; font-size: 13px; font-weight: 600; font-family: 'Segoe UI', Arial, sans-serif; border: 1px solid #e0e0e0;">Go to Portal</a>
                    <!--<![endif]-->
                  </td>
                </tr>
              </table>

              <!-- Portal features -->
              <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="margin-bottom: 24px;">
                <tr>
                  <td bgcolor="#f8f9fc" style="background-color: #f8f9fc; border-radius: 8px; padding: 20px;">
                    <p style="margin: 0 0 10px; font-size: 13px; font-weight: 600; color: #1a1a2e; font-family: 'Segoe UI', Arial, sans-serif;">From your portal you can:</p>
                    <table role="presentation" cellpadding="0" cellspacing="0" width="100%">
                      <tr><td style="padding: 3px 0; font-size: 13px; color: #555555; font-family: 'Segoe UI', Arial, sans-serif;">&#8226; View your assigned networks and access policies</td></tr>
                      <tr><td style="padding: 3px 0; font-size: 13px; color: #555555; font-family: 'Segoe UI', Arial, sans-serif;">&#8226; Generate enrollment tokens for additional devices</td></tr>
                      <tr><td style="padding: 3px 0; font-size: 13px; color: #555555; font-family: 'Segoe UI', Arial, sans-serif;">&#8226; Enable MFA with an authenticator app</td></tr>
                      <tr><td style="padding: 3px 0; font-size: 13px; color: #555555; font-family: 'Segoe UI', Arial, sans-serif;">&#8226; Change your password</td></tr>
                    </table>
                  </td>
                </tr>
              </table>

              <!-- Footer -->
              <table role="presentation" cellpadding="0" cellspacing="0" width="100%">
                <tr>
                  <td style="border-top: 1px solid #eeeeee; padding-top: 20px; text-align: center;">
                    <p style="color: #999999; font-size: 12px; margin: 0; font-family: 'Segoe UI', Arial, sans-serif;">
                      If you have questions, contact your administrator.
                    </p>
                  </td>
                </tr>
              </table>

            </td>
          </tr>

        </table>
      </td>
    </tr>
  </table>
</body>
</html>
"""

    return _send_email(to_email, subject, text_body, html_body, from_header=_onboarding_from())

