import { cn } from '@/lib/utils'
import logoSvg from '@/assets/apertera-logo.svg?raw'

// The asset pins its lettering to white (style="color:#fff"); drop that so the
// lettering follows the theme text color through currentColor. The mark keeps
// its own brand teal. The wrapper carries the accessible name, so the inner
// <title> and its id are not needed.
const markup = logoSvg
  .replace(/\sstyle="color:#fff"/, '')
  .replace(/\srole="img"\saria-labelledby="title"/, ' aria-hidden="true" focusable="false"')
  .replace(/<title[^>]*>.*?<\/title>/s, '')

/** The Apertera logo (mark + wordmark) from src/assets/apertera-logo.svg. */
export function AperteraLogo({ className }: { className?: string }) {
  return (
    <span
      role="img"
      aria-label="Apertera"
      className={cn('inline-flex h-6 text-gray-900 dark:text-gray-100 [&>svg]:h-full [&>svg]:w-auto', className)}
      dangerouslySetInnerHTML={{ __html: markup }}
    />
  )
}
