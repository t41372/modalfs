import { CircleCheckIcon, InfoIcon, Loader2Icon, OctagonXIcon, TriangleAlertIcon } from 'lucide-react'
import { Toaster as Sonner, type ToasterProps } from 'sonner'

/** Toasts. They follow the `dark` class on <html>, which the app sets from its own appearance setting. */
const Toaster = (props: ToasterProps) => {
  const dark = typeof document !== 'undefined' && document.documentElement.classList.contains('dark')
  return (
    <Sonner
      theme={dark ? 'dark' : 'light'}
      className="toaster group"
      icons={{
        success: <CircleCheckIcon className="size-4 text-ok" />,
        info: <InfoIcon className="size-4" />,
        warning: <TriangleAlertIcon className="size-4 text-warn" />,
        error: <OctagonXIcon className="size-4 text-destructive" />,
        loading: <Loader2Icon className="size-4 animate-spin" />,
      }}
      style={
        {
          '--normal-bg': 'var(--popover)',
          '--normal-text': 'var(--popover-foreground)',
          '--normal-border': 'var(--border)',
          '--border-radius': 'calc(var(--radius) * 1.4)',
        } as React.CSSProperties
      }
      toastOptions={{ classNames: { toast: 'cn-toast !text-[13px] !shadow-[0_8px_30px_-12px_rgb(0_0_0/0.25)]' } }}
      {...props}
    />
  )
}

export { Toaster }
