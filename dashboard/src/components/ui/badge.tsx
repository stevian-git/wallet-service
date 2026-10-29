import * as React from "react"
import { cn } from "@/lib/utils"

function Badge({
  className,
  variant = "default",
  ...props
}: React.ComponentProps<"div"> & {
  variant?: "default" | "secondary" | "destructive" | "outline" | "success" | "warning"
}) {
  const variants: Record<string, string> = {
    default: "border-transparent bg-primary text-primary-foreground shadow-sm [a&]:hover:bg-primary/90",
    secondary: "border-transparent bg-secondary text-secondary-foreground [a&]:hover:bg-secondary/80",
    destructive: "border-transparent bg-destructive text-white shadow-sm [a&]:hover:bg-destructive/90",
    outline: "text-foreground",
    success: "border-transparent bg-green-500 text-white shadow-sm",
    warning: "border-transparent bg-yellow-500 text-white shadow-sm",
  }

  return (
    <div
      data-slot="badge"
      className={cn(
        "inline-flex items-center rounded-md border px-2.5 py-0.5 text-xs font-semibold transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
        variants[variant],
        className
      )}
      {...props}
    />
  )
}

export { Badge }
